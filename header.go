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
	le := func(v any) error { return binary.Write(w, binary.LittleEndian, v) }

	if err := le([]byte(Magic)); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	if err := le(h.Rows); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	if err := le(uint16(len(h.Cols))); err != nil {
		return fmt.Errorf("tblx: write: %w", err)
	}
	for _, c := range h.Cols {
		if err := le(uint8(len(c.Name))); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if err := le([]byte(c.Name)); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if err := le(uint8(c.Type)); err != nil {
			return fmt.Errorf("tblx: write: %w", err)
		}
		if err := le(uint8(0)); err != nil { // flags, reserved
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

	le := func(v any) error {
		if err := binary.Read(r, binary.LittleEndian, v); err != nil {
			return fmt.Errorf("tblx: unexpected end of file: %w", err)
		}
		return nil
	}

	if err := le(&h.Rows); err != nil {
		return h, err
	}
	var nCols uint16
	if err := le(&nCols); err != nil {
		return h, err
	}

	h.Cols = make([]ColumnDef, nCols)
	for i := range h.Cols {
		var nameLen uint8
		if err := le(&nameLen); err != nil {
			return h, err
		}
		name := make([]byte, nameLen)
		if _, err := io.ReadFull(r, name); err != nil {
			return h, fmt.Errorf("tblx: truncated column name at index %d: %w", i, err)
		}
		var dt, flags uint8
		if err := le(&dt); err != nil {
			return h, err
		}
		if err := le(&flags); err != nil { // reserved, ignored
			return h, err
		}
		if err := DataType(dt).Validate(); err != nil {
			return h, fmt.Errorf("tblx: column %q: %w", string(name), err)
		}
		h.Cols[i] = ColumnDef{Name: string(name), Type: DataType(dt)}
	}
	return h, nil
}
