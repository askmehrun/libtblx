#!/usr/bin/env python3
"""Demo of the tblx Python binding — every operation runs through the Go core.

Run it from the libtblx root:

    make python-example          # builds libtblx.so first
    # or manually:
    make lib && python3 python/example.py
"""

import os
import tempfile

import tblx


def main() -> None:
    print("tblx core version (Go):", tblx.version())

    here = os.path.dirname(os.path.abspath(__file__))
    csv_path = os.path.join(here, "..", "samples", "test.csv")
    out = os.path.join(tempfile.gettempdir(), "tblx-demo.tblx")

    # 1. CSV -> Table, types guessed by the Go core ----------------------
    t = tblx.from_csv(csv_path)
    print(f"from_csv: {t.nrows} rows x {t.ncols} cols — {t.schema()}")

    # 2. Write it through the Go writer, read it back --------------------
    tblx.write(out, t.names, t.types, t.columns)
    print(f"wrote {out} ({os.path.getsize(out)} bytes)")

    t2 = tblx.read(out)
    for row in t2.to_dicts():
        print("   ", row)

    # 3. Fast path: CSV -> TBL conversion entirely inside Go --------------
    fast = os.path.join(tempfile.gettempdir(), "tblx-demo-fast.tblx")
    tblx.import_csv(csv_path, fast)
    print(f"import_csv -> {fast} ({os.path.getsize(fast)} bytes)")

    # 4. Back to CSV (rendered by the Go core) -----------------------------
    print("to_csv:")
    print(tblx.to_csv(t2))


if __name__ == "__main__":
    main()
