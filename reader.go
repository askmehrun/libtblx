package tblx

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
)

// Reader provides random, column-granular access to an open TBLX file.
// NewReader parses only the header and the length array, so opening a
// file is O(columns) regardless of row count; data is read lazily via
// ReadColumn / ReadAll.
//
// Reader methods are safe for concurrent use.
type Reader struct {
	file     *os.File
	ColNames []string
	ColTypes []DataType
	NRows    uint64
	NCols    uint16

	colLens     []uint64 // byte size of each column's data block
	colOffsets  []int64  // absolute start offset of each column's data block
	dataStart   int64    // absolute offset of the first data byte
	fileSize    int64
	mu          sync.Mutex
}

// NewReader opens path, validates the magic bytes and parses the header
// plus the length array. The file stays open; call Close when done.
func NewReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("tblx: open %s: %w", path, err)
	}
	r := &Reader{file: f}
	if err := r.init(path); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

// init parses the header and the seek index (length array).
func (r *Reader) init(path string) error {
	h, err := readHeader(r.file)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	r.NRows = h.Rows
	r.NCols = uint16(len(h.Cols))
	r.ColNames = make([]string, r.NCols)
	r.ColTypes = make([]DataType, r.NCols)
	for i, c := range h.Cols {
		r.ColNames[i] = c.Name
		r.ColTypes[i] = c.Type
	}

	// The length array — this is what makes columns seekable.
	r.colLens = make([]uint64, r.NCols)
	buf := make([]byte, 8)
	for i := range r.colLens {
		if _, err := io.ReadFull(r.file, buf); err != nil {
			return fmt.Errorf("%s: truncated length array: %w", path, err)
		}
		r.colLens[i] = binary.LittleEndian.Uint64(buf)
	}
	pos, err := r.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("tblx: seek: %w", err)
	}
	r.dataStart = pos

	// Capture file size for length validation.
	fi, err := r.file.Stat()
	if err != nil {
		return fmt.Errorf("tblx: stat: %w", err)
	}
	r.fileSize = fi.Size()

	// Precompute absolute offsets so ReadColumn is O(1).
	r.colOffsets = make([]int64, r.NCols)
	r.colOffsets[0] = r.dataStart
	for i := 1; i < len(r.colOffsets); i++ {
		r.colOffsets[i] = r.colOffsets[i-1] + int64(r.colLens[i-1])
	}

	if err := r.checkTailMagic(); err != nil {
		return err
	}
	return nil
}

// Header returns the file's decoded header (row count + column defs).
func (r *Reader) Header() Header {
	h := Header{Rows: r.NRows, Cols: make([]ColumnDef, r.NCols)}
	for i := range h.Cols {
		h.Cols[i] = ColumnDef{Name: r.ColNames[i], Type: r.ColTypes[i]}
	}
	return h
}

// ColLen returns the on-disk byte size of column colIndex's data block.
func (r *Reader) ColLen(colIndex int) (uint64, error) {
	if colIndex < 0 || colIndex >= int(r.NCols) {
		return 0, fmt.Errorf("tblx: column index %d out of range [0, %d)", colIndex, r.NCols)
	}
	return r.colLens[colIndex], nil
}

// ReadColumn decodes every value of column colIndex and returns them as
// a []any of int64, float64 or string depending on the column's type.
// Only that column's bytes are read — the payoff of the columnar layout.
func (r *Reader) ReadColumn(colIndex int) ([]any, error) {
	if colIndex < 0 || colIndex >= int(r.NCols) {
		return nil, fmt.Errorf("tblx: column index %d out of range [0, %d)", colIndex, r.NCols)
	}
	name := r.ColNames[colIndex]
	dt := r.ColTypes[colIndex]

	cdc, err := codecFor(dt)
	if err != nil {
		return nil, err
	}

	// Read and decompress the column's data block.
	blockData, err := r.readBlock(colIndex)
	if err != nil {
		return nil, fmt.Errorf("tblx: column %q: %w", name, err)
	}

	vals := make([]any, r.NRows)
	buf := bytes.NewReader(blockData)
	for row := uint64(0); row < r.NRows; row++ {
		v, err := cdc.decode(buf)
		if err != nil {
			return nil, fmt.Errorf("tblx: column %q row %d: %w", name, row+1, err)
		}
		vals[row] = v
	}
	return vals, nil
}

// readBlock seeks to a column's data block and returns the decompressed bytes.
// The first byte is a compression marker: 0x1f = gzip, 0x00 = raw.
func (r *Reader) readBlock(colIndex int) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	off := r.colOffsets[colIndex]
	length := r.colLens[colIndex]

	// Validate declared block length against actual file size.
	if off+int64(length) > r.fileSize {
		return nil, fmt.Errorf("corrupt length array: column %d declares %d bytes at offset %d but file is only %d bytes", colIndex, length, off, r.fileSize)
	}

	if _, err := r.file.Seek(off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to column: %w", err)
	}

	block := make([]byte, length)
	if _, err := io.ReadFull(r.file, block); err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}

	switch block[0] {
	case 0x1f: // gzip
		zr, err := gzip.NewReader(bytes.NewReader(block[1:]))
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer zr.Close()
		return io.ReadAll(zr)
	case 0x00: // raw
		return block[1:], nil
	default:
		return nil, fmt.Errorf("unknown compression marker 0x%02x", block[0])
	}
}

// Rows returns an iterator that yields one row at a time as a map from
// column name to value. Columns are read from disk once and held in
// memory; rows are produced lazily without allocating the full result
// slice up front. Use this instead of ReadAll for large tables.
func (r *Reader) Rows() RowIterator {
	return RowIterator{reader: r}
}

// RowIterator iterates over table rows one at a time.
type RowIterator struct {
	reader   *Reader
	cols     [][]any
	row      uint64
	err      error
	finished bool
}

// Next advances to the next row. Returns false when all rows have been
// consumed or on error. Call Err() after Next returns false to check
// for errors.
func (it *RowIterator) Next() bool {
	if it.finished {
		return false
	}
	if it.cols == nil {
		it.cols = make([][]any, it.reader.NCols)
		for i := range it.cols {
			vals, err := it.reader.ReadColumn(i)
			if err != nil {
				it.err = fmt.Errorf("tblx: read column %q: %w", it.reader.ColNames[i], err)
				return false
			}
			it.cols[i] = vals
		}
	}
	if it.row >= it.reader.NRows {
		it.finished = true
		return false
	}
	return true
}

// Row returns the current row as a map from column name to value.
// Call Next() first. The returned map is valid until the next call to
// Next().
func (it *RowIterator) Row() map[string]any {
	m := make(map[string]any, len(it.cols))
	for ci, vals := range it.cols {
		m[it.reader.ColNames[ci]] = vals[it.row]
	}
	it.row++
	return m
}

// Err returns any error encountered during iteration.
func (it *RowIterator) Err() error {
	return it.err
}

// Close releases the column data held by the iterator. Call when done
// iterating before all rows have been consumed.
func (it *RowIterator) Close() {
	it.cols = nil
	it.finished = true
}

// ReadAll decodes the whole table and transposes it into rows: each row
// is a map from column name to value. For large tables, use Rows()
// instead to avoid allocating all rows at once.
func (r *Reader) ReadAll() ([]map[string]any, error) {
	it := r.Rows()
	defer it.Close()
	var rows []map[string]any
	for it.Next() {
		rows = append(rows, it.Row())
	}
	return rows, it.Err()
}

// Close releases the underlying file handle.
func (r *Reader) Close() error {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("tblx: close: %w", err)
	}
	return nil
}

// checkTailMagic validates the trailing magic at EOF to detect truncation.
func (r *Reader) checkTailMagic() error {
	tail := make([]byte, 4)
	off := r.dataStart + int64(r.totalDataLen())
	if _, err := r.file.ReadAt(tail, off); err != nil {
		return fmt.Errorf("tblx: file appears truncated (cannot read tail magic): %w", err)
	}
	if string(tail) != TailMagic {
		return fmt.Errorf("tblx: bad tail magic % x, want % x — file is corrupted or truncated", tail, []byte(TailMagic))
	}
	return nil
}

func (r *Reader) totalDataLen() uint64 {
	var sum uint64
	for _, l := range r.colLens {
		sum += l
	}
	return sum
}
