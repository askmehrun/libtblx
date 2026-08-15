package tblx

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Reader provides random, column-granular access to an open TBLX file.
// NewReader parses only the header and the length array, so opening a
// file is O(columns) regardless of row count; data is read lazily via
// ReadColumn / ReadAll.
type Reader struct {
	file     *os.File
	ColNames []string
	ColTypes []DataType
	NRows    uint64
	NCols    uint16

	colLens   []uint64 // byte size of each column's data block
	dataStart int64    // absolute offset of the first data byte
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
	for i := range r.colLens {
		if err := binary.Read(r.file, binary.LittleEndian, &r.colLens[i]); err != nil {
			return fmt.Errorf("%s: truncated length array: %w", path, err)
		}
	}
	pos, err := r.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("tblx: seek: %w", err)
	}
	r.dataStart = pos
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
	name, dt := r.ColNames[colIndex], r.ColTypes[colIndex]

	cdc, err := codecFor(dt)
	if err != nil {
		return nil, err
	}
	// Fixed-width blocks have a known size — verify before decoding.
	if cdc.fixedSize > 0 && r.colLens[colIndex] != uint64(cdc.fixedSize)*r.NRows {
		return nil, fmt.Errorf("tblx: column %q: length array mismatch (%d bytes, want %d)",
			name, r.colLens[colIndex], uint64(cdc.fixedSize)*r.NRows)
	}

	// Blocks are back-to-back in definition order, so the requested one
	// starts after all previous blocks.
	off := r.dataStart
	for i := 0; i < colIndex; i++ {
		off += int64(r.colLens[i])
	}
	if _, err := r.file.Seek(off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("tblx: seek to column %q: %w", name, err)
	}

	vals := make([]any, r.NRows)
	for row := uint64(0); row < r.NRows; row++ {
		v, err := cdc.decode(r.file)
		if err != nil {
			return nil, fmt.Errorf("tblx: column %q row %d: %w", name, row+1, err)
		}
		vals[row] = v
	}
	return vals, nil
}

// ReadAll decodes the whole table and transposes it into rows: each row
// is a map from column name to value.
func (r *Reader) ReadAll() ([]map[string]any, error) {
	cols := make([][]any, r.NCols)
	for i := range cols {
		vals, err := r.ReadColumn(i)
		if err != nil {
			return nil, fmt.Errorf("tblx: read column %q: %w", r.ColNames[i], err)
		}
		cols[i] = vals
	}

	rows := make([]map[string]any, r.NRows)
	for ri := uint64(0); ri < r.NRows; ri++ {
		m := make(map[string]any, r.NCols)
		for ci := range cols {
			m[r.ColNames[ci]] = cols[ci][ri]
		}
		rows[ri] = m
	}
	return rows, nil
}

// Close releases the underlying file handle.
func (r *Reader) Close() error {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("tblx: close: %w", err)
	}
	return nil
}
