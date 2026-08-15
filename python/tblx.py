"""tblx — Python binding for the TBLX format, powered by the Go core.

This module is a thin ``ctypes`` bridge to ``libtblx.so`` — the Tablix Go
library compiled with ``-buildmode=c-shared``. There is **no Python
re-implementation of the format**: every read, write, type guess and CSV
conversion runs the exact same Go code the tblx CLI uses.

Build the shared library once (from the libtblx root):

    make lib                # produces libtblx.so (+ libtblx.h for C users)

Then, from anywhere:

    import tblx

    t = tblx.from_csv("samples/test.csv")   # types guessed by the Go core
    tblx.write("out.tblx", t.names, t.types, t.columns)

    t2 = tblx.read("out.tblx")
    print(t2.schema(), t2.nrows)
    for row in t2.to_dicts()[:3]:
        print(row)

If the library is not next to this file, point the loader at it:

    TBLX_LIB=/usr/local/lib/libtblx.so python3 my_script.py
"""

from __future__ import annotations

import ctypes
import os
import struct
import tempfile
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Sequence, Union

__all__ = [
    "INT", "FLOAT", "STRING", "TblxError", "Table",
    "version", "read", "write", "import_csv", "from_csv", "to_csv",
    "type_name",
]

#: Column type codes — the same wire values the Go library uses.
INT = 1
FLOAT = 2
STRING = 3

_TYPE_NAMES = {INT: "int", FLOAT: "float", STRING: "string"}
_NAME_TYPES = {v: k for k, v in _TYPE_NAMES.items()}

TypeLike = Union[int, str]


class TblxError(RuntimeError):
    """Raised when the Go library reports an error."""


def type_name(dt: int) -> str:
    """Return "int", "float" or "string" for a type code."""
    return _TYPE_NAMES.get(dt, "unknown")


def _norm_type(t: TypeLike) -> int:
    if isinstance(t, str):
        try:
            return _NAME_TYPES[t]
        except KeyError:
            raise TblxError(f"unknown type {t!r}; use 'int', 'float' or 'string'") from None
    if t not in _TYPE_NAMES:
        raise TblxError(f"invalid data type {t}")
    return t


# ---------------------------------------------------------------------------
# Shared library loading
# ---------------------------------------------------------------------------

def _candidate_paths() -> List[str]:
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.dirname(here)  # libtblx/
    cands: List[str] = []
    env = os.environ.get("TBLX_LIB") or os.environ.get("TBL_LIB")
    if env:
        cands.append(env)
    cands += [
        os.path.join(root, "libtblx.so"),
        os.path.join(root, "libtblx.dylib"),
        os.path.join(here, "libtblx.so"),
        os.path.join(root, "libtblx.dll"),
        "libtblx.so",
    ]
    return cands


def _load() -> ctypes.CDLL:
    for path in _candidate_paths():
        if os.path.exists(path):
            lib = ctypes.CDLL(path)
            _declare(lib)
            return lib
    raise TblxError(
        "libtblx.so not found. Build it from the libtblx root with `make lib` "
        "(or set TBLX_LIB=/path/to/libtblx.so)."
    )


def _declare(lib: ctypes.CDLL) -> None:
    c64, c32 = ctypes.c_int64, ctypes.c_int32
    lib.tbl_version.restype = ctypes.c_void_p
    lib.tbl_last_error.restype = ctypes.c_void_p
    lib.tbl_free.argtypes = [ctypes.c_void_p]
    lib.tbl_open.argtypes = [ctypes.c_char_p]
    lib.tbl_open.restype = c64
    lib.tbl_close.argtypes = [c64]
    lib.tbl_rows.argtypes = [c64]
    lib.tbl_rows.restype = c64
    lib.tbl_cols.argtypes = [c64]
    lib.tbl_cols.restype = c32
    lib.tbl_col_name.argtypes = [c64, c32]
    lib.tbl_col_name.restype = ctypes.c_void_p
    lib.tbl_col_type.argtypes = [c64, c32]
    lib.tbl_col_type.restype = c32
    lib.tbl_col_data.argtypes = [c64, c32, ctypes.POINTER(c64)]
    lib.tbl_col_data.restype = ctypes.c_void_p
    lib.tbl_export_csv.argtypes = [c64]
    lib.tbl_export_csv.restype = ctypes.c_void_p
    lib.tbl_import_csv.argtypes = [ctypes.c_char_p] * 3
    lib.tbl_import_csv.restype = c32
    lib.tbl_guess_types.argtypes = [ctypes.c_char_p]
    lib.tbl_guess_types.restype = ctypes.c_void_p
    lib.tbl_writer_new.argtypes = [c64, c32]
    lib.tbl_writer_new.restype = c64
    lib.tbl_writer_set_col.argtypes = [c64, c32, ctypes.c_char_p, c32, ctypes.c_void_p, c64]
    lib.tbl_writer_set_col.restype = c32
    lib.tbl_writer_finish.argtypes = [c64, ctypes.c_char_p]
    lib.tbl_writer_finish.restype = c32
    lib.tbl_writer_free.argtypes = [c64]


class _Lib:
    """Lazy singleton around the shared library."""

    def __init__(self) -> None:
        self._lib: Optional[ctypes.CDLL] = None

    @property
    def c(self) -> ctypes.CDLL:
        if self._lib is None:
            self._lib = _load()
        return self._lib

    def error(self) -> str:
        ptr = self.c.tbl_last_error()
        return _bytes_at(ptr).decode("utf-8", "replace") if ptr else ""

    def take_str(self, ptr: Optional[int]) -> str:
        """Decode a C string the library returned, then free it."""
        if not ptr:
            raise TblxError(self.error() or "tblx: unknown error")
        try:
            return _bytes_at(ptr).decode("utf-8")
        finally:
            self.c.tbl_free(ptr)

    def take_bytes(self, ptr: Optional[int], n: int) -> bytes:
        """Copy n bytes out of a library buffer, then free it."""
        if not ptr:
            raise TblxError(self.error() or "tblx: unknown error")
        try:
            return _bytes_at(ptr, n)
        finally:
            self.c.tbl_free(ptr)


def _bytes_at(ptr: int, n: Optional[int] = None) -> bytes:
    if n is None:
        return ctypes.string_at(ptr)
    return ctypes.string_at(ptr, n)


_lib = _Lib()


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

def version() -> str:
    """Version of the loaded Go core, e.g. "1.0.0"."""
    return _lib.take_str(_lib.c.tbl_version())


@dataclass
class Table:
    """A decoded TBLX table: names, type codes and column-major data."""

    names: List[str]
    types: List[int]
    columns: List[List[Any]]

    @property
    def nrows(self) -> int:
        """Number of rows (0 for a table without columns)."""
        return len(self.columns[0]) if self.columns else 0

    @property
    def ncols(self) -> int:
        """Number of columns."""
        return len(self.names)

    def schema(self) -> str:
        """One-line schema summary, e.g. ``name:string age:int score:float``."""
        return " ".join(f"{n}:{type_name(t)}" for n, t in zip(self.names, self.types))

    def rows(self) -> List[List[Any]]:
        """Transposed, row-major view of the data."""
        return [list(row) for row in zip(*self.columns)] if self.columns else []

    def to_dicts(self) -> List[Dict[str, Any]]:
        """Rows as ``{column name: value}`` dictionaries."""
        return [dict(zip(self.names, row)) for row in self.rows()]


def read(path: str) -> Table:
    """Read a TBLX file through the Go library and return a Table."""
    handle = _lib.c.tbl_open(path.encode("utf-8"))
    if handle < 0:
        raise TblxError(_lib.error())
    try:
        nrows = _lib.c.tbl_rows(handle)
        ncols = _lib.c.tbl_cols(handle)
        names: List[str] = []
        types: List[int] = []
        columns: List[List[Any]] = []
        for ci in range(ncols):
            names.append(_lib.take_str(_lib.c.tbl_col_name(handle, ci)))
            types.append(_lib.c.tbl_col_type(handle, ci))

            out_len = ctypes.c_int64(0)
            ptr = _lib.c.tbl_col_data(handle, ci, ctypes.byref(out_len))
            buf = _lib.take_bytes(ptr, out_len.value)
            columns.append(_decode_column(types[-1], buf, nrows))
        return Table(names, types, columns)
    finally:
        _lib.c.tbl_close(handle)


def _decode_column(dt: int, buf: bytes, nrows: int) -> List[Any]:
    if dt == INT:
        return list(struct.unpack(f"<{nrows}q", buf)) if nrows else []
    if dt == FLOAT:
        return list(struct.unpack(f"<{nrows}d", buf)) if nrows else []
    # string: uint32 length + UTF-8 bytes, back to back
    out: List[Any] = []
    off = 0
    for _ in range(nrows):
        (ln,) = struct.unpack_from("<I", buf, off)
        off += 4
        out.append(buf[off:off + ln].decode("utf-8"))
        off += ln
    return out


def _encode_column(name: str, dt: int, values: Sequence[Any]) -> bytes:
    if dt == INT:
        for i, v in enumerate(values):
            if isinstance(v, bool) or not isinstance(v, int):
                raise TblxError(f"column {name!r} row {i + 1}: {v!r} is not an int")
        return struct.pack(f"<{len(values)}q", *values)
    if dt == FLOAT:
        for i, v in enumerate(values):
            if isinstance(v, bool) or not isinstance(v, (int, float)):
                raise TblxError(f"column {name!r} row {i + 1}: {v!r} is not a float")
        return struct.pack(f"<{len(values)}d", *[float(v) for v in values])
    parts: List[bytes] = []
    for i, v in enumerate(values):
        if not isinstance(v, str):
            raise TblxError(f"column {name!r} row {i + 1}: {v!r} is not a str")
        b = v.encode("utf-8")
        parts.append(struct.pack("<I", len(b)))
        parts.append(b)
    return b"".join(parts)


def write(path: str, names: Sequence[str], types: Sequence[TypeLike],
          columns: Sequence[Sequence[Any]]) -> None:
    """Write a table to *path*; the bytes are produced by the Go writer."""
    if not names:
        raise TblxError("table must have at least one column")
    if len(names) != len(types) or len(names) != len(columns):
        raise TblxError("names, types and columns must have the same length")
    nrows = len(columns[0])
    codes = [_norm_type(t) for t in types]
    for i, (name, col) in enumerate(zip(names, columns)):
        if len(col) != nrows:
            raise TblxError(f"column {name!r} has {len(col)} rows, expected {nrows}")
        nb = name.encode("utf-8")
        if not nb or len(nb) > 255:
            raise TblxError(f"column names must be 1-255 bytes, got {name!r}")

    handle = _lib.c.tbl_writer_new(nrows, len(names))
    if handle < 0:
        raise TblxError(_lib.error())
    try:
        for ci, (name, dt, col) in enumerate(zip(names, codes, columns)):
            buf = _encode_column(name, dt, col)
            ok = _lib.c.tbl_writer_set_col(handle, ci, name.encode("utf-8"), dt, buf, len(buf))
            if ok != 0:
                raise TblxError(_lib.error())
        if _lib.c.tbl_writer_finish(handle, path.encode("utf-8")) != 0:
            raise TblxError(_lib.error())
    finally:
        _lib.c.tbl_writer_free(handle)


def import_csv(csv_path: str, tbl_path: str,
               types: Optional[Sequence[TypeLike]] = None) -> str:
    """Convert CSV → TBL entirely inside the Go library.

    When *types* is omitted the Go core guesses each column's type.
    Returns *tbl_path*.
    """
    types_csv = ""
    if types is not None:
        types_csv = ",".join(type_name(_norm_type(t)) for t in types)
    ok = _lib.c.tbl_import_csv(
        csv_path.encode("utf-8"), tbl_path.encode("utf-8"), types_csv.encode("utf-8")
    )
    if ok != 0:
        raise TblxError(_lib.error())
    return tbl_path


def from_csv(csv_path: str, types: Optional[Sequence[TypeLike]] = None) -> Table:
    """Read a CSV file (first record = header) into a Table via the Go core."""
    fd, tmp = tempfile.mkstemp(suffix=".tblx")
    os.close(fd)
    try:
        import_csv(csv_path, tmp, types)
        return read(tmp)
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass


def to_csv(table: Table, path: Optional[str] = None) -> str:
    """Render a table as CSV text using the Go core; writes it when *path*
    is given, and always returns the text."""
    fd, tmp = tempfile.mkstemp(suffix=".tblx")
    os.close(fd)
    try:
        write(tmp, table.names, table.types, table.columns)
        handle = _lib.c.tbl_open(tmp.encode("utf-8"))
        if handle < 0:
            raise TblxError(_lib.error())
        try:
            text = _lib.take_str(_lib.c.tbl_export_csv(handle))
        finally:
            _lib.c.tbl_close(handle)
    finally:
        try:
            os.unlink(tmp)
        except OSError:
            pass
    if path is not None:
        with open(path, "w", encoding="utf-8", newline="") as f:
            f.write(text)
    return text
