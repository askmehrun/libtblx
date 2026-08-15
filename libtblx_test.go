package tblx

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestGoldenExample pins the exact bytes of the spec's worked example:
// 2 rows × 2 columns (name string, age int). If this ever changes, the
// spec, the website and every binding are wrong — not this test.
func TestGoldenExample(t *testing.T) {
	want := []byte{
		0x54, 0x42, 0x4C, 0x58, // magic "TBLX"
		0x02, 0, 0, 0, 0, 0, 0, 0, // row_count = 2
		0x02, 0x00, // col_count = 2
		0x04, 'n', 'a', 'm', 'e', 0x03, 0x00, // "name", string, flags=0
		0x03, 'a', 'g', 'e', 0x01, 0x00, // "age", int, flags=0
		0x10, 0, 0, 0, 0, 0, 0, 0, // lengths[0] = 16
		0x10, 0, 0, 0, 0, 0, 0, 0, // lengths[1] = 16
		0x05, 0, 0, 0, 'A', 'l', 'i', 'c', 'e', // "Alice"
		0x03, 0, 0, 0, 'B', 'o', 'b', // "Bob"
		0x1E, 0, 0, 0, 0, 0, 0, 0, // 30
		0x19, 0, 0, 0, 0, 0, 0, 0, // 25
	}

	path := filepath.Join(t.TempDir(), "golden.tblx")
	w, err := NewWriter(
		[]string{"name", "age"},
		[]DataType{DTypeString, DTypeInt},
		[][]any{{"Alice", "Bob"}, {int64(30), int64(25)}},
	)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("bytes mismatch\ngot:  % x\nwant: % x", got, want)
	}
}

// TestRoundTrip exercises every type, unicode strings, the TBLX
// "missing value" defaults, and negative numbers.
func TestRoundTrip(t *testing.T) {
	names := []string{"name", "age", "score", "note"}
	types := []DataType{DTypeString, DTypeInt, DTypeFloat, DTypeString}
	data := [][]any{
		{"Alice", "Bob", "مریم", ""},
		{int64(30), int64(25), int64(0), int64(-7)},
		{95.5, 87.0, 0.0, -1.25},
		{"", "ok", "unicode ✓", "x"},
	}

	path := filepath.Join(t.TempDir(), "rt.tblx")
	w, err := NewWriter(names, types, data)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	if r.NRows != 4 || r.NCols != 4 {
		t.Fatalf("shape = %d×%d, want 4×4", r.NRows, r.NCols)
	}
	if !reflect.DeepEqual(r.ColNames, names) || !reflect.DeepEqual(r.ColTypes, types) {
		t.Fatalf("header = %v %v", r.ColNames, r.ColTypes)
	}
	for c := range names {
		vals, err := r.ReadColumn(c)
		if err != nil {
			t.Fatalf("ReadColumn(%d): %v", c, err)
		}
		if !reflect.DeepEqual(vals, data[c]) {
			t.Errorf("column %q = %v, want %v", names[c], vals, data[c])
		}
	}

	// Fixed-width blocks must match rows × 8 exactly.
	if cl, _ := r.ColLen(1); cl != 32 {
		t.Errorf("ColLen(age) = %d, want 32", cl)
	}
}

func TestRejectsBadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.tblx")
	if err := os.WriteFile(path, []byte("NOPE and some trailing bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewReader(path)
	if err == nil || !strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("want a bad-magic error, got %v", err)
	}
}

func TestRejectsUnknownType(t *testing.T) {
	_, err := NewWriter([]string{"x"}, []DataType{9}, [][]any{{int64(1)}})
	if err == nil || !strings.Contains(err.Error(), "invalid data type") {
		t.Fatalf("want an invalid-type error, got %v", err)
	}
}

func TestGuessTypes(t *testing.T) {
	got := GuessTypes([][]string{
		{"1", "2", ""},       // all ints (empty ignored)
		{"1", "2.5", "3"},    // has a float
		{"a", "1", ""},       // has a string
		{"", ""},             // nothing to judge
	})
	want := []DataType{DTypeInt, DTypeFloat, DTypeString, DTypeString}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GuessTypes = %v, want %v", got, want)
	}
}
