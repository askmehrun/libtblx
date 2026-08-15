// Command clib compiles the Tablix Go library into a C shared library
// (libtblx.so / libtblx.dylib / libtblx.dll) that any FFI-capable language
// can call. It is what the Python binding in ../python drives.
//
// Build it with:
//
//	make lib            # or:
//	CGO_ENABLED=1 go build -buildmode=c-shared -o libtblx.so ./clib
//
// The build also emits libtblx.h with the full C declarations. The
// exported symbols keep the short `tbl_` prefix (the format itself is
// TBLX); every function that can fail returns a negative value (or a
// NULL pointer) and stores a human-readable message retrievable via
// tbl_last_error. Strings and byte buffers returned to the caller are
// heap-allocated and must be released with tbl_free.
//
// Column data crosses the boundary as packed little-endian bytes, using
// exactly the TBLX in-block encodings:
//
//	int    -> 8 bytes per value  (int64, little-endian)
//	float  -> 8 bytes per value  (float64, little-endian)
//	string -> per value: uint32 length + UTF-8 bytes
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"unsafe"

	tblx "github.com/askmehrun/libtblx"
)

// ---- handle registries ------------------------------------------------------

var (
	mu      sync.Mutex
	readers = map[int64]*tblx.Reader{}
	writers = map[int64]*writerState{}
	nextID  int64 = 1
	lastErr string
)

type writerState struct {
	nRows    int
	names    []string
	types    []tblx.DataType
	columns  [][]any
	finished bool
}

func setErr(err error) {
	if err != nil {
		lastErr = err.Error()
	} else {
		lastErr = ""
	}
}

func cstr(s string) *C.char { return C.CString(s) }

func getReader(h C.int64_t) (*tblx.Reader, bool) {
	r, ok := readers[int64(h)]
	return r, ok
}

// readCSV is a small helper shared by the import/guess entry points.
func readCSV(path string) (headers []string, rows [][]string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("tblx: parse csv: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, fmt.Errorf("tblx: %s: empty CSV", path)
	}
	return recs[0], recs[1:], nil
}

// transpose turns row-major CSV data into column-major samples, padding
// ragged rows with empty strings.
func transpose(headers []string, rows [][]string) [][]string {
	cols := make([][]string, len(headers))
	for c := range cols {
		col := make([]string, len(rows))
		for ri, row := range rows {
			if c < len(row) {
				col[ri] = row[c]
			}
		}
		cols[c] = col
	}
	return cols
}

// ---- meta --------------------------------------------------------------------

//export tbl_version
// tbl_version returns the Tablix library version, e.g. "1.0.0".
func tbl_version() *C.char { return cstr(tblx.Version) }

//export tbl_last_error
// tbl_last_error returns the message of the most recent failed call.
func tbl_last_error() *C.char {
	mu.Lock()
	defer mu.Unlock()
	return cstr(lastErr)
}

//export tbl_free
// tbl_free releases any string or buffer returned by this library.
func tbl_free(p unsafe.Pointer) {
	if p != nil {
		C.free(p)
	}
}

// ---- reading -------------------------------------------------------------------

//export tbl_open
// tbl_open opens a TBLX file and returns a handle >= 0, or -1 on error.
func tbl_open(path *C.char) C.int64_t {
	mu.Lock()
	defer mu.Unlock()
	r, err := tblx.NewReader(C.GoString(path))
	if err != nil {
		setErr(err)
		return -1
	}
	id := nextID
	nextID++
	readers[id] = r
	setErr(nil)
	return C.int64_t(id)
}

//export tbl_close
// tbl_close closes a reader handle.
func tbl_close(h C.int64_t) {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := readers[int64(h)]; ok {
		r.Close()
		delete(readers, int64(h))
	}
}

//export tbl_rows
// tbl_rows returns the table's row count.
func tbl_rows(h C.int64_t) C.int64_t {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := getReader(h); ok {
		return C.int64_t(r.NRows)
	}
	return -1
}

//export tbl_cols
// tbl_cols returns the table's column count.
func tbl_cols(h C.int64_t) C.int32_t {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := getReader(h); ok {
		return C.int32_t(r.NCols)
	}
	return -1
}

//export tbl_col_name
// tbl_col_name returns column i's name (caller frees), or NULL.
func tbl_col_name(h C.int64_t, i C.int32_t) *C.char {
	mu.Lock()
	defer mu.Unlock()
	r, ok := getReader(h)
	if !ok || int(i) < 0 || int(i) >= int(r.NCols) {
		setErr(fmt.Errorf("tblx: invalid handle or column index %d", int(i)))
		return nil
	}
	return cstr(r.ColNames[i])
}

//export tbl_col_type
// tbl_col_type returns column i's type code (1 int, 2 float, 3 string) or -1.
func tbl_col_type(h C.int64_t, i C.int32_t) C.int32_t {
	mu.Lock()
	defer mu.Unlock()
	r, ok := getReader(h)
	if !ok || int(i) < 0 || int(i) >= int(r.NCols) {
		setErr(fmt.Errorf("tblx: invalid handle or column index %d", int(i)))
		return -1
	}
	return C.int32_t(r.ColTypes[i])
}

//export tbl_col_data
// tbl_col_data returns column i's values as packed little-endian bytes
// (see the package doc for the encodings). out_len receives the byte
// count. The caller frees the buffer with tbl_free. Returns NULL on error.
func tbl_col_data(h C.int64_t, i C.int32_t, outLen *C.int64_t) unsafe.Pointer {
	mu.Lock()
	defer mu.Unlock()
	r, ok := getReader(h)
	if !ok {
		setErr(fmt.Errorf("tblx: invalid handle %d", int64(h)))
		return nil
	}
	vals, err := r.ReadColumn(int(i))
	if err != nil {
		setErr(err)
		return nil
	}

	var buf []byte
	switch r.ColTypes[i] {
	case tblx.DTypeInt:
		buf = make([]byte, 8*len(vals))
		for k, v := range vals {
			binary.LittleEndian.PutUint64(buf[k*8:], uint64(v.(int64)))
		}
	case tblx.DTypeFloat:
		buf = make([]byte, 8*len(vals))
		for k, v := range vals {
			binary.LittleEndian.PutUint64(buf[k*8:], math.Float64bits(v.(float64)))
		}
	default: // string
		for _, v := range vals {
			s := v.(string)
			var lb [4]byte
			binary.LittleEndian.PutUint32(lb[:], uint32(len(s)))
			buf = append(buf, lb[:]...)
			buf = append(buf, s...)
		}
	}

	*outLen = C.int64_t(len(buf))
	if len(buf) == 0 {
		// Never hand back NULL for a valid-but-empty column.
		return C.CBytes([]byte{0})
	}
	return C.CBytes(buf)
}

//export tbl_export_csv
// tbl_export_csv renders the whole table as CSV text (caller frees).
func tbl_export_csv(h C.int64_t) *C.char {
	mu.Lock()
	defer mu.Unlock()
	r, ok := getReader(h)
	if !ok {
		setErr(fmt.Errorf("tblx: invalid handle %d", int64(h)))
		return nil
	}

	cols := make([][]any, r.NCols)
	for ci := range cols {
		vals, err := r.ReadColumn(ci)
		if err != nil {
			setErr(err)
			return nil
		}
		cols[ci] = vals
	}

	var sb strings.Builder
	w := csv.NewWriter(&sb)
	if err := w.Write(r.ColNames); err != nil {
		setErr(err)
		return nil
	}
	for ri := uint64(0); ri < r.NRows; ri++ {
		rec := make([]string, r.NCols)
		for ci := range rec {
			rec[ci] = tblx.FormatValue(cols[ci][ri])
		}
		if err := w.Write(rec); err != nil {
			setErr(err)
			return nil
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		setErr(err)
		return nil
	}
	setErr(nil)
	return cstr(sb.String())
}

// ---- CSV import -------------------------------------------------------------

//export tbl_import_csv
// tbl_import_csv converts a CSV file (first record = header) straight to
// a TBLX file. types_csv is a comma-separated list like "string,int,float";
// pass "" to let the library guess each column's type. Returns 0 on success.
func tbl_import_csv(csvPath, tblPath, typesCSV *C.char) C.int32_t {
	mu.Lock()
	defer mu.Unlock()

	src := C.GoString(csvPath)
	headers, rows, err := readCSV(src)
	if err != nil {
		setErr(err)
		return -1
	}

	var types []tblx.DataType
	if s := strings.TrimSpace(C.GoString(typesCSV)); s != "" {
		for _, part := range strings.Split(s, ",") {
			switch strings.TrimSpace(part) {
			case "int":
				types = append(types, tblx.DTypeInt)
			case "float":
				types = append(types, tblx.DTypeFloat)
			case "string":
				types = append(types, tblx.DTypeString)
			default:
				setErr(fmt.Errorf("tblx: unknown type %q (want int, float or string)", part))
				return -1
			}
		}
		if len(types) != len(headers) {
			setErr(fmt.Errorf("tblx: %d headers but %d types given", len(headers), len(types)))
			return -1
		}
	} else {
		types = tblx.GuessTypes(transpose(headers, rows))
	}

	data, err := tblx.ConvertCSV(headers, rows, types)
	if err != nil {
		setErr(err)
		return -1
	}
	w, err := tblx.NewWriter(headers, types, data)
	if err != nil {
		setErr(err)
		return -1
	}
	if err := w.Write(C.GoString(tblPath)); err != nil {
		setErr(err)
		return -1
	}
	setErr(nil)
	return 0
}

//export tbl_guess_types
// tbl_guess_types infers column types of a CSV file and returns them as a
// comma-separated string like "string,int,float" (caller frees), or NULL.
func tbl_guess_types(csvPath *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()

	headers, rows, err := readCSV(C.GoString(csvPath))
	if err != nil {
		setErr(err)
		return nil
	}
	names := make([]string, len(headers))
	for i, t := range tblx.GuessTypes(transpose(headers, rows)) {
		names[i] = t.String()
	}
	setErr(nil)
	return cstr(strings.Join(names, ","))
}

// ---- writing -----------------------------------------------------------------

//export tbl_writer_new
// tbl_writer_new starts a write session for nrows × ncols; returns a handle.
func tbl_writer_new(nrows C.int64_t, ncols C.int32_t) C.int64_t {
	mu.Lock()
	defer mu.Unlock()
	if int64(nrows) < 0 || int(ncols) <= 0 || int(ncols) > 0xFFFF {
		setErr(fmt.Errorf("tblx: invalid table shape %d x %d", int64(nrows), int(ncols)))
		return -1
	}
	id := nextID
	nextID++
	writers[id] = &writerState{
		nRows:   int(nrows),
		names:   make([]string, int(ncols)),
		types:   make([]tblx.DataType, int(ncols)),
		columns: make([][]any, int(ncols)),
	}
	setErr(nil)
	return C.int64_t(id)
}

//export tbl_writer_set_col
// tbl_writer_set_col feeds one column's packed data (same encodings as
// tbl_col_data). Returns 0 on success.
func tbl_writer_set_col(h C.int64_t, idx C.int32_t, name *C.char, dtype C.int32_t, data unsafe.Pointer, dataLen C.int64_t) C.int32_t {
	mu.Lock()
	defer mu.Unlock()
	ws, ok := writers[int64(h)]
	if !ok {
		setErr(fmt.Errorf("tblx: invalid writer handle %d", int64(h)))
		return -1
	}
	if ws.finished {
		setErr(fmt.Errorf("tblx: writer already finished"))
		return -1
	}
	i := int(idx)
	if i < 0 || i >= len(ws.names) {
		setErr(fmt.Errorf("tblx: column index %d out of range", i))
		return -1
	}
	dt := tblx.DataType(dtype)
	if err := dt.Validate(); err != nil {
		setErr(err)
		return -1
	}

	buf := C.GoBytes(data, C.int(dataLen))
	n := ws.nRows
	col := make([]any, n)

	switch dt {
	case tblx.DTypeInt:
		if len(buf) != 8*n {
			setErr(fmt.Errorf("tblx: column %q: expected %d bytes of int64 data, got %d", C.GoString(name), 8*n, len(buf)))
			return -1
		}
		for k := 0; k < n; k++ {
			col[k] = int64(binary.LittleEndian.Uint64(buf[k*8:]))
		}
	case tblx.DTypeFloat:
		if len(buf) != 8*n {
			setErr(fmt.Errorf("tblx: column %q: expected %d bytes of float64 data, got %d", C.GoString(name), 8*n, len(buf)))
			return -1
		}
		for k := 0; k < n; k++ {
			col[k] = math.Float64frombits(binary.LittleEndian.Uint64(buf[k*8:]))
		}
	default: // string
		off := 0
		for k := 0; k < n; k++ {
			if off+4 > len(buf) {
				setErr(fmt.Errorf("tblx: column %q: truncated string data", C.GoString(name)))
				return -1
			}
			l := int(binary.LittleEndian.Uint32(buf[off:]))
			off += 4
			if off+l > len(buf) {
				setErr(fmt.Errorf("tblx: column %q: truncated string data", C.GoString(name)))
				return -1
			}
			col[k] = string(buf[off : off+l])
			off += l
		}
	}

	ws.names[i] = C.GoString(name)
	ws.types[i] = dt
	ws.columns[i] = col
	setErr(nil)
	return 0
}

//export tbl_writer_finish
// tbl_writer_finish validates the accumulated table and writes it to path.
func tbl_writer_finish(h C.int64_t, path *C.char) C.int32_t {
	mu.Lock()
	defer mu.Unlock()
	ws, ok := writers[int64(h)]
	if !ok {
		setErr(fmt.Errorf("tblx: invalid writer handle %d", int64(h)))
		return -1
	}
	if ws.finished {
		setErr(fmt.Errorf("tblx: writer already finished"))
		return -1
	}
	for c, name := range ws.names {
		if name == "" {
			setErr(fmt.Errorf("tblx: column %d was never set via tbl_writer_set_col", c))
			return -1
		}
	}
	w, err := tblx.NewWriter(ws.names, ws.types, ws.columns)
	if err != nil {
		setErr(err)
		return -1
	}
	if err := w.Write(C.GoString(path)); err != nil {
		setErr(err)
		return -1
	}
	ws.finished = true
	setErr(nil)
	return 0
}

//export tbl_writer_free
// tbl_writer_free drops a writer handle.
func tbl_writer_free(h C.int64_t) {
	mu.Lock()
	defer mu.Unlock()
	delete(writers, int64(h))
}

func main() {}
