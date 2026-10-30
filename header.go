package tblx

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ColumnDef is one entry of the file's column definition list: a name
// and the type of its values.
type ColumnDef struct {
	Name string
	Type DataType
}

// Header is the decoded TBLX file header: the row count and the column
// definitions. The length array is intentionally NOT part of it — those
// numbers are data-dependent, so the writer emits them as placeholders
// and the reader treats them as a seek index (see writer.go / reader.go).
type Header struct {
	Rows uint64
	Cols []ColumnDef
}

// validate enforces the format limits that a writer must respect.
func (h Header) validate() error {
	if len(h.Cols) == 0 {
		return fmt.Errorf("tblx: table must have at least one column")
	}
	if len(h.Cols) > 0xFFFF {
		return fmt.Errorf("tblx: too many columns: %d (max 65535)", len(h.Cols))
	}
	for _, c := range h.Cols {
		if len(c.Name) == 0 {
			return fmt.Errorf("tblx: column names must not be empty")
		}
		if len(c.Name) > 255 {
			return fmt.Errorf("tblx: column name %q is %d bytes; the format caps names at 255", c.Name, len(c.Name))
		}
		if err := c.Type.Validate(); err != nil {
			return fmt.Errorf("tblx: column %q: %w", c.Name, err)
		}
	}
	return nil
}

// writeHeader serialises magic, row count, column count and the column
// definitions to w — everything before the length array.
func writeHeader(w io.Writer, h Header) error {
	writeU64 := func(v uint64) error {
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, v)
		_, err := w.Write(buf)
		return err
	}
	writeU16 := func(v uint16) error {
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, v)
		_, err := w.Write(buf)
		return err
	}
	writeU8 := func(v uint8) error {
		_, err := w.Write([]byte{v})
		return err
	}

	if _, err := w.Write([]byte(Magic)); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	if err := writeU64(h.Rows); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	if err := writeU16(uint16(len(h.Cols))); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	for _, c := range h.Cols {
		if err := writeU8(uint8(len(c.Name))); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if _, err := w.Write([]byte(c.Name)); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if err := writeU8(uint8(c.Type)); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if err := writeU8(0); err != nil { // flags, reserved
			return fmt.Errorf("tblx: write: %w", err)
		}
	}
	return nil
}

// readHeader parses exactly what writeHeader emits. It stops before the
// length array; the caller's stream is positioned at its first byte.
func readHeader(r io.Reader) (Header, error) {
	var h Header

	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return h, fmt.Errorf("tblx: too short to be a TBLX file")
	}
	if string(magic) != Magic {
		return h, fmt.Errorf("tblx: bad magic % x, want % x (%q)", magic, []byte(Magic), Magic)
	}

	readU64 := func() (uint64, error) {
		buf := make([]byte, 8)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, fmt.Errorf("tblx: unexpected end of file: %w", err)
		}
		return binary.LittleEndian.Uint64(buf), nil
	}
	readU16 := func() (uint16, error) {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, fmt.Errorf("tblx: unexpected end of file: %w", err)
		}
		return binary.LittleEndian.Uint16(buf), nil
	}
	readU8 := func() (uint8, error) {
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, fmt.Errorf("tblx: unexpected end of file: %w", err)
		}
		return buf[0], nil
	}

	rows, err := readU64()
	if err != nil {
		return h, err
	}
	h.Rows = rows

	nCols, err := readU16()
	if err != nil {
		return h, err
	}

	h.Cols = make([]ColumnDef, nCols)
	for i := range h.Cols {
		nameLen, err := readU8()
		if err != nil {
			return h, err
		}
		name := make([]byte, nameLen)
		if _, err := io.ReadFull(r, name); err != nil {
			return h, fmt.Errorf("tblx: truncated column name at index %d: %w", i, err)
		}
		dt, err := readU8()
		if err != nil {
			return h, err
		}
		flags, err := readU8() // reserved, ignored
		if err != nil {
			return h, err
		}
		_ = flags
		if err := DataType(dt).Validate(); err != nil {
			return h, fmt.Errorf("tblx: column %q: %w", string(name), err)
		}
		h.Cols[i] = ColumnDef{Name: string(name), Type: DataType(dt)}
	}
	return h, nil
}
