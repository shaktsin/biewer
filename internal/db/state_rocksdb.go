//go:build rocksdb && cgo

package db

/*
#cgo pkg-config: rocksdb
#include <stdlib.h>
#include <rocksdb/c.h>
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

type rocksStateBackend struct {
	db      *C.rocksdb_t
	options *C.rocksdb_options_t
	read    *C.rocksdb_readoptions_t
	write   *C.rocksdb_writeoptions_t
}

func openStateBackend(rocksPath, _ string) (stateBackend, error) {
	if err := os.MkdirAll(rocksPath, 0o755); err != nil {
		return nil, fmt.Errorf("create RocksDB directory: %w", err)
	}
	options := C.rocksdb_options_create()
	C.rocksdb_options_set_create_if_missing(options, 1)
	path := C.CString(rocksPath)
	defer C.free(unsafe.Pointer(path))
	var cErr *C.char
	database := C.rocksdb_open(options, path, &cErr)
	if cErr != nil {
		message := C.GoString(cErr)
		C.rocksdb_free(unsafe.Pointer(cErr))
		C.rocksdb_options_destroy(options)
		return nil, fmt.Errorf("open RocksDB: %s", message)
	}
	return &rocksStateBackend{
		db: database, options: options,
		read: C.rocksdb_readoptions_create(), write: C.rocksdb_writeoptions_create(),
	}, nil
}

func (b *rocksStateBackend) Put(key string, value []byte) error {
	keyBytes := []byte(key)
	cKey := C.CBytes(keyBytes)
	cValue := C.CBytes(value)
	defer C.free(cKey)
	defer C.free(cValue)
	var cErr *C.char
	C.rocksdb_put(b.db, b.write, (*C.char)(cKey), C.size_t(len(keyBytes)), (*C.char)(cValue), C.size_t(len(value)), &cErr)
	return rocksError(cErr)
}

func (b *rocksStateBackend) Get(key string) ([]byte, error) {
	keyBytes := []byte(key)
	cKey := C.CBytes(keyBytes)
	defer C.free(cKey)
	var valueLen C.size_t
	var cErr *C.char
	value := C.rocksdb_get(b.db, b.read, (*C.char)(cKey), C.size_t(len(keyBytes)), &valueLen, &cErr)
	if err := rocksError(cErr); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	defer C.rocksdb_free(unsafe.Pointer(value))
	return C.GoBytes(unsafe.Pointer(value), C.int(valueLen)), nil
}

func (b *rocksStateBackend) Close() error {
	C.rocksdb_readoptions_destroy(b.read)
	C.rocksdb_writeoptions_destroy(b.write)
	C.rocksdb_close(b.db)
	C.rocksdb_options_destroy(b.options)
	return nil
}

func (b *rocksStateBackend) Name() string { return "rocksdb" }

func rocksError(cErr *C.char) error {
	if cErr == nil {
		return nil
	}
	message := C.GoString(cErr)
	C.rocksdb_free(unsafe.Pointer(cErr))
	return fmt.Errorf("RocksDB: %s", message)
}
