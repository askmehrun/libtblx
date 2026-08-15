package tblx

import "fmt"

// DataType is the on-disk element type of a column. Its numeric value is
// part of the wire format and must never change; new types always take
// the next free code.
type DataType uint8

// Column types defined by the TBLX specification.
const (
	// DTypeInt stores each value as an 8-byte signed little-endian int64.
	DTypeInt DataType = 1
	// DTypeFloat stores each value as an 8-byte IEEE 754 double (LE).
	DTypeFloat DataType = 2
	// DTypeString stores each value as a uint32 LE length followed by
	// that many UTF-8 bytes, with no null terminator.
	DTypeString DataType = 3
)

// String returns the short human-readable name of the type:
// "int", "float", "string", or "unknown" for anything else.
func (d DataType) String() string {
	switch d {
	case DTypeInt:
		return "int"
	case DTypeFloat:
		return "float"
	case DTypeString:
		return "string"
	default:
		return "unknown"
	}
}

// Validate reports whether d is a type this build knows how to encode.
// Readers use it to reject files with unknown type codes.
func (d DataType) Validate() error {
	if _, ok := codecs[d]; ok {
		return nil
	}
	return fmt.Errorf("invalid data type %d (known: 1=int, 2=float, 3=string)", uint8(d))
}

// Next returns the next type in the cycle int -> float -> string -> int.
// The import wizard uses it to rotate a column's type with one keypress.
func (d DataType) Next() DataType {
	switch d {
	case DTypeInt:
		return DTypeFloat
	case DTypeFloat:
		return DTypeString
	default:
		return DTypeInt
	}
}

// Prev returns the previous type in the cycle (string -> float -> int -> string).
func (d DataType) Prev() DataType {
	switch d {
	case DTypeInt:
		return DTypeString
	case DTypeFloat:
		return DTypeInt
	default:
		return DTypeFloat
	}
}
