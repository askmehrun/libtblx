// Package tblx implements the TBLX binary columnar file format as a
// small, dependency-free Go library.
//
// A TBLX file is:
//
//	magic "TBLX" (4 B) · row_count u64 · col_count u16
//	column defs: name_len u8 · name · type u8 · flags u8 (=0)
//	length array: col_count × u64          ← the seek index
//	data blocks: one per column, back-to-back
//
// Little-endian throughout, no padding, no NULLs (missing values are
// stored as 0 / 0.0 / ""). The complete spec lives in the project wiki
// (tblx/wiki/Spec.md).
//
// # Usage
//
//	import tblx "github.com/askmehrun/libtblx"
//
//	r, _ := tblx.NewReader("data.tblx")
//	defer r.Close()
//	ages, _ := r.ReadColumn(1) // seeks straight to column 1
//
// # Extending the format core
//
// Per-type encodings are concentrated in ONE place: the codec table in
// column.go. Adding a new DataType takes three small steps — a constant
// in type.go, one entry in that table, and a conversion rule in csv.go.
// Writer, reader, C bridge and tests pick the new type up automatically.
//
// The same core compiles to a C shared library (see ./clib) that powers
// the Python binding and can serve any FFI-capable language.
package tblx

// Magic is the 4-byte ASCII signature that begins every TBLX file:
// 0x54 0x42 0x4C 0x58.
const Magic = "TBLX"

// Version is the library / format revision.
const Version = "1.0.0"
