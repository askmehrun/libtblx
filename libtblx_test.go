package tblx

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestGoldenExample verifies the file header layout: magic, row count,
// column count, and column definitions. Data blocks are gzip-compressed,
// so their exact bytes depend on the compression library.
func TestGoldenExample(t *testing.T) {
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

	// Verify header: magic "TBLX"
	if string(got[0:4]) != "TBLX" {
		t.Errorf("bad magic: % x", got[0:4])
	}
	// row_count = 2
	if binary.LittleEndian.Uint64(got[4:12]) != 2 {
		t.Errorf("bad row count: %d", binary.LittleEndian.Uint64(got[4:12]))
	}
	// col_count = 2
	if binary.LittleEndian.Uint16(got[12:14]) != 2 {
		t.Errorf("bad col count: %d", binary.LittleEndian.Uint16(got[12:14]))
	}
	// First column def: name_len=4, "name", type=3 (string)
	if got[14] != 4 || string(got[15:19]) != "name" || got[19] != 3 {
		t.Errorf("bad first column def: % x", got[14:20])
	}
	// Second column def: name_len=3, "age", type=1 (int)
	if got[21] != 3 || string(got[22:25]) != "age" || got[25] != 1 {
		t.Errorf("bad second column def: % x", got[21:26])
	}
	// Tail magic
	if !bytes.HasSuffix(got, []byte(TailMagic)) {
		t.Errorf("missing tail magic")
	}

	// Verify round-trip correctness.
	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	names, err := r.ReadColumn(0)
	if err != nil || names[0] != "Alice" || names[1] != "Bob" {
		t.Errorf("name column wrong: %v (%v)", names, err)
	}
	ages, err := r.ReadColumn(1)
	if err != nil || ages[0] != int64(30) || ages[1] != int64(25) {
		t.Errorf("age column wrong: %v (%v)", ages, err)
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

	// Block includes 1-byte compression marker + data; should be <= raw size + 1.
	if cl, _ := r.ColLen(1); cl > 33 {
		t.Errorf("ColLen(age) = %d, want <= 33 (1 marker byte + 32 raw)", cl)
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
