# libtblx — build targets
#
#   make lib             compile the Go core to a C shared library
#                        (libtblx.so + libtblx.h), used by python/tblx.py
#   make python-example  run the Python binding against the fresh library
#   make vet             go vet the module

LIB := libtblx.so

.PHONY: all lib python-example vet clean

all: lib

# Shared library for Python / C / any FFI-capable language.
lib:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(LIB) ./clib

python-example: lib
	TBLX_LIB=./$(LIB) python3 python/example.py

vet:
	go vet ./...

clean:
	rm -f $(LIB) libtblx.h
