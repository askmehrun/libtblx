package tblx

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestConcurrentReadColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.tblx")
	w, err := NewWriter(
		[]string{"a", "b", "c"},
		[]DataType{DTypeInt, DTypeFloat, DTypeString},
		[][]any{
			{int64(1), int64(2), int64(3)},
			{1.0, 2.0, 3.0},
			{"x", "y", "z"},
		},
	)
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

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func(col int) { defer wg.Done(); _, _ = r.ReadColumn(col) }(0)
		go func(col int) { defer wg.Done(); _, _ = r.ReadColumn(col) }(1)
		go func(col int) { defer wg.Done(); _, _ = r.ReadColumn(col) }(2)
	}
	wg.Wait()
}

func TestCorruptLengthArrayDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.tblx")
	w, err := NewWriter(
		[]string{"a"},
		[]DataType{DTypeInt},
		[][]any{{int64(1), int64(2)}},
	)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Corrupt the length array: read file, modify a length byte to be huge.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Header ends at col defs; find where length array starts by parsing.
	// Magic(4) + rows(8) + cols(2) + coldef(name_len(1)+name+type(1)+flags(1))
	// For "a": 1+1+1+1 = 4 bytes. So length array starts at 4+8+2+4 = 18.
	lenOff := 18
	binary.LittleEndian.PutUint64(data[lenOff:], ^uint64(0)) // max uint64
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = NewReader(path)
	if err == nil {
		t.Fatal("expected error for corrupt length array")
	}
	if !strings.Contains(err.Error(), "truncated") && !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRowIterator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iter.tblx")
	w, err := NewWriter(
		[]string{"name", "age"},
		[]DataType{DTypeString, DTypeInt},
		[][]any{{"Alice", "Bob", "Carol"}, {int64(30), int64(25), int64(35)}},
	)
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

	it := r.Rows()
	defer it.Close()

	var names []string
	for it.Next() {
		row := it.Row()
		names = append(names, row["name"].(string))
	}
	if it.Err() != nil {
		t.Fatalf("iterator error: %v", it.Err())
	}
	want := []string{"Alice", "Bob", "Carol"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// TestRowIteratorEarlyStop verifies iteration can stop before the last row
// (Close releases the per-column streams without a full table load).
func TestRowIteratorEarlyStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iter_stop.tblx")
	w, err := NewWriter(
		[]string{"a"},
		[]DataType{DTypeInt},
		[][]any{{int64(1), int64(2), int64(3), int64(4), int64(5)}},
	)
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

	it := r.Rows()
	defer it.Close()
	var got []int64
	for it.Next() {
		if len(got) == 2 {
			break
		}
		got = append(got, it.Row()["a"].(int64))
	}
	if it.Err() != nil {
		t.Fatalf("iterator error: %v", it.Err())
	}
	want := []int64{1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("early-stop rows = %v, want %v", got, want)
	}
}

// TestRowIteratorCorruptReported verifies a decode error on one row is
// surfaced via Err() rather than crashing the loop.
func TestRowIteratorCorruptReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iter_corrupt.tblx")
	w, err := NewWriter(
		[]string{"a"},
		[]DataType{DTypeString},
		[][]any{{"x", "y", "z"}},
	)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Corrupt the first string value's length prefix in place, keeping the
	// file size (and trailing magic) intact so NewReader still opens it but
	// the last row fails to decode.
	data, _ := os.ReadFile(path)
	// data block starts at offset 26; first byte is the gzip marker, so the
	// first value's uint32 length prefix sits at 27.
	binary.LittleEndian.PutUint32(data[27:], 1000)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	it := r.Rows()
	defer it.Close()
	for it.Next() {
		it.Row()
	}
	if it.Err() == nil {
		t.Fatal("expected a decode error from truncated file, got none")
	}
}
