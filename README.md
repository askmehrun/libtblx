# libtblx — the TBLX format, as a library

`libtblx` is the reference implementation of **TBLX** (the format behind
**Tablix**, pronounced *tab-lix*): a minimal binary columnar file format.

- a **Go library** (this module's root package, `package tblx`) — stdlib only
- a **C shared library** (`./clib` → `libtblx.so`, built with `-buildmode=c-shared`)
- a **Python binding** (`./python/tblx.py`) that drives the C library via ctypes

The [tblx CLI](https://github.com/askmehrun/tblx) is
built around this module; the Python binding calls the *same compiled Go
code*, so every language produces byte-identical files.

## The format, in one breath

```
magic "TBLX" (4 B) · row_count u64 · col_count u16
column defs: name_len u8 · name · type u8 (1=int64 2=float64 3=string) · flags u8 (=0)
length array: col_count × u64        ← byte size of each block = the seek index
data blocks: one per column, back-to-back
```

Little-endian throughout, no padding, no NULLs (missing → `0` / `0.0` / `""`).
The complete spec: [wiki/Spec](https://github.com/askmehrun/tblx/wiki/Spec).

## Go

```go
import tblx "github.com/askmehrun/libtblx"

w, _ := tblx.NewWriter(
    []string{"name", "age"},
    []tblx.DataType{tblx.DTypeString, tblx.DTypeInt},
    [][]any{{"Alice", "Bob"}, {int64(30), int64(25)}},
)
w.Write("people.tblx")

r, _ := tblx.NewReader("people.tblx")
defer r.Close()
ages, _ := r.ReadColumn(1)          // seeks straight to column 1
rows, _ := r.ReadAll()              // or decode everything
```

## C bridge

```sh
make lib        # → libtblx.so + libtblx.h
```

Exported surface: `tbl_open / tbl_close / tbl_rows / tbl_cols /
tbl_col_name / tbl_col_type / tbl_col_data / tbl_export_csv /
tbl_import_csv / tbl_guess_types / tbl_writer_new / tbl_writer_set_col /
tbl_writer_finish / tbl_writer_free / tbl_last_error / tbl_free /
tbl_version`. Columns cross the boundary as packed little-endian bytes —
exactly the TBLX in-block encodings. Returned pointers are freed with
`tbl_free`.

## Python

```sh
make lib                      # build libtblx.so once
python3 python/example.py     # full round-trip demo
```

```python
import tblx

t = tblx.from_csv("samples/test.csv")       # types guessed by the Go core
tblx.write("demo.tblx", t.names, t.types, t.columns)
t2 = tblx.read("demo.tblx")
ages = t2.columns[t2.names.index("age")]
```

If the library lives elsewhere: `TBLX_LIB=/path/to/libtblx.so`.

## Repository layout — and where to extend what

The core is deliberately split so each concern has exactly one home:

```
libtblx/
├── doc.go             package overview, Magic, Version
├── type.go            DataType constants, validation, the ←/→ cycle
├── header.go          Header/ColumnDef + their (de)serialisation
├── column.go          THE codec table — add a type here (one entry)
├── writer.go          placeholders for lengths, back-filled after blocks
├── reader.go          header parse + seekable column reads + integrity checks
├── csv.go             ConvertCSV + GuessTypes
├── value.go           FormatValue — one renderer for every consumer
├── libtblx_test.go    golden-byte + round-trip tests (run: go test ./...)
├── clib/clib.go       cgo bridge → libtblx.so (any FFI language)
├── python/tblx.py     ctypes binding, zero Python-side format logic
├── python/example.py  round-trip demo
├── samples/test.csv   demo data (includes missing values)
└── Makefile           make lib · make python-example · make vet
```

**Adding a new data type** takes three steps: a constant in `type.go`,
one entry in the `codecs` table in `column.go`, and a conversion rule in
`csv.go` — writer, reader, tests, bridge and binding pick it up for free.
See [wiki/Extending](https://github.com/askmehrun/tblx/wiki/Extending).

## License

MIT
