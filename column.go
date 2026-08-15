package tblx

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// codec describes the on-disk encoding of one DataType.
//
// This table is THE extension point of the format core: to support a new
// type, add a constant in type.go and one entry here (plus a conversion
// rule in csv.go). Writer, reader and the integrity checks below all key
// off this registry — nothing else changes.
type codec struct {
	// fixedSize is the on-disk width of one value in bytes, or 0 for
	// variable-width types (string) where every value carries its own
	// uint32 length prefix.
	fixedSize int

	// encode writes a single value. Errors are context-free; callers
	// add the column/row.
	encode func(w io.Writer, v any) error

	// decode reads a single value.
	decode func(r io.Reader) (any, error)
}

// codecs is the registry of on-disk encodings, keyed by DataType.
var codecs = map[DataType]codec{
	DTypeInt:    {fixedSize: 8, encode: encodeInt, decode: decodeInt},
	DTypeFloat:  {fixedSize: 8, encode: encodeFloat, decode: decodeFloat},
	DTypeString: {fixedSize: 0, encode: encodeString, decode: decodeString},
}

// codecFor looks up the encoding for dt.
func codecFor(dt DataType) (codec, error) {
	c, ok := codecs[dt]
	if !ok {
		return codec{}, fmt.Errorf("tblx: %w", dt.Validate())
	}
	return c, nil
}

// ---- int ---------------------------------------------------------------

func encodeInt(w io.Writer, v any) error {
	var iv int64
	switch n := v.(type) {
	case int64:
		iv = n
	case int:
		iv = int64(n)
	default:
		return fmt.Errorf("value %v is %T, want int64", v, v)
	}
	return binary.Write(w, binary.LittleEndian, iv)
}

func decodeInt(r io.Reader) (any, error) {
	var v int64
	if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ---- float ---------------------------------------------------------------

func encodeFloat(w io.Writer, v any) error {
	var fv float64
	switch n := v.(type) {
	case float64:
		fv = n
	case float32:
		fv = float64(n)
	case int:
		fv = float64(n)
	case int64:
		fv = float64(n)
	default:
		return fmt.Errorf("value %v is %T, want float64", v, v)
	}
	return binary.Write(w, binary.LittleEndian, fv)
}

func decodeFloat(r io.Reader) (any, error) {
	var v float64
	if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ---- string ---------------------------------------------------------------

func encodeString(w io.Writer, v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("value %v is %T, want string", v, v)
	}
	if uint64(len(s)) > math.MaxUint32 {
		return fmt.Errorf("string of %d bytes exceeds the uint32 length limit", len(s))
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(s))); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func decodeString(r io.Reader) (any, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return string(buf), nil
}
