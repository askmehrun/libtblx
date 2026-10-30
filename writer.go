package tblx

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Writer serialises an in-memory columnar table to a TBLX file.
// Build one with NewWriter, then call Write exactly once.
type Writer struct {
	header  Header
	colData [][]any
}

// NewWriter validates the table shape and returns a Writer ready to
// serialise it. All columns must carry the same number of values, every
// data type must be known (see column.go), and names must fit the
// format's 255-byte limit (see Header.validate).
func NewWriter(colNames []string, colTypes []DataType, colData [][]any) (*Writer, error) {
	if len(colNames) != len(colTypes) || len(colNames) != len(colData) {
		return nil, fmt.Errorf("tblx: inconsistent column counts: %d names, %d types, %d data slices",
			len(colNames), len(colTypes), len(colData))
	}

	h := Header{Cols: make([]ColumnDef, len(colNames))}
	for i := range colNames {
		h.Cols[i] = ColumnDef{Name: colNames[i], Type: colTypes[i]}
	}
	if err := h.validate(); err != nil {
		return nil, err
	}

	if len(colData) > 0 {
		h.Rows = uint64(len(colData[0]))
		for i, col := range colData {
			if uint64(len(col)) != h.Rows {
				return nil, fmt.Errorf("tblx: column %q has %d rows, column %q has %d: all columns must match",
					colNames[i], len(col), colNames[0], h.Rows)
			}
		}
	}

	return &Writer{header: h, colData: colData}, nil
}

// Write serialises the table to path atomically: it writes to a temp file
// in the same directory and renames on success, so a crash or error never
// leaves a partially-written target file.
func (w *Writer) Write(path string) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tblx-tmp-*")
	if err != nil {
		return fmt.Errorf("tblx: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpName)
		}
	}()

	if err := w.encodeTo(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tblx: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("tblx: rename to %s: %w", path, err)
	}
	return nil
}

// encodeTo produces the whole file on any io.WriteSeeker — an *os.File
// in production, a test buffer in memory.
//
// Column lengths are unknown until every block is written, so zero
// placeholders are emitted first and patched by seeking back; the data
// itself is never buffered twice.
func (w *Writer) encodeTo(out io.WriteSeeker) error {
	offset := func() (int64, error) {
		pos, err := out.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, fmt.Errorf("tblx: seek: %w", err)
		}
		return pos, nil
	}

	if err := writeHeader(out, w.header); err != nil {
		return err
	}

	// Length array placeholders — back-filled once the blocks exist.
	lengthsAt, err := offset()
	if err != nil {
		return err
	}
	for range w.header.Cols {
		if err := binary.Write(out, binary.LittleEndian, uint64(0)); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
	}

	// Data blocks, back-to-back in definition order.
	// Each block is gzip-compressed when that makes it smaller; the
	// first byte of each block indicates compression (0x1f = gzip, 0x00 = raw).
	blockStart := make([]int64, len(w.header.Cols))
	for c, col := range w.colData {
		def := w.header.Cols[c]
		cdc, err := codecFor(def.Type)
		if err != nil {
			return err
		}

		// Encode the column into a buffer first.
		var raw bytes.Buffer
		for row, v := range col {
			if err := cdc.encode(&raw, v); err != nil {
				return fmt.Errorf("tblx: column %q row %d: %w", def.Name, row+1, err)
			}
		}

		// Try gzip; use it only if smaller than raw.
		var comp bytes.Buffer
		zw := gzip.NewWriter(&comp)
		if _, err := zw.Write(raw.Bytes()); err != nil {
			zw.Close()
			return fmt.Errorf("tblx: column %q: %w", def.Name, err)
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("tblx: column %q gzip close: %w", def.Name, err)
		}

		start, err := offset()
		if err != nil {
			return err
		}
		blockStart[c] = start

		if comp.Len() < raw.Len() {
			// Write compressed: marker byte + gzip stream.
			if _, err := out.Write([]byte{0x1f}); err != nil {
				return err
			}
			if _, err := out.Write(comp.Bytes()); err != nil {
				return err
			}
		} else {
			// Write raw: marker byte + uncompressed data.
			if _, err := out.Write([]byte{0x00}); err != nil {
				return err
			}
			if _, err := out.Write(raw.Bytes()); err != nil {
				return err
			}
		}
	}

	// Back-fill the length array, then leave the cursor at EOF.
	end, err := offset()
	if err != nil {
		return err
	}
	for c := range w.header.Cols {
		blockEnd := end
		if c+1 < len(blockStart) {
			blockEnd = blockStart[c+1]
		}
		if _, err := out.Seek(lengthsAt+int64(c)*8, io.SeekStart); err != nil {
			return fmt.Errorf("tblx: seek to length slot %d: %w", c, err)
		}
		if err := binary.Write(out, binary.LittleEndian, uint64(blockEnd-blockStart[c])); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
	}
	if _, err := out.Seek(end, io.SeekStart); err != nil {
		return fmt.Errorf("tblx: seek to end: %w", err)
	}

	// Trailing magic for integrity: lets readers detect truncated files.
	buf := make([]byte, 4)
	copy(buf, []byte(TailMagic))
	if _, err := out.Write(buf); err != nil {
		return fmt.Errorf("tblx: write tail magic: %w", err)
	}
	return nil
}
