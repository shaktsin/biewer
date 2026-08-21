//go:build !rocksdb || !cgo

package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type fileStateBackend struct {
	dir string
}

func openStateBackend(_ string, fallbackDir string) (stateBackend, error) {
	if err := os.MkdirAll(fallbackDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &fileStateBackend{dir: fallbackDir}, nil
}

func (b *fileStateBackend) path(key string) string {
	return filepath.Join(b.dir, strings.ReplaceAll(key, "/", "-")+".json")
}

func (b *fileStateBackend) Put(key string, value []byte) error {
	path := b.path(key)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, value, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (b *fileStateBackend) Get(key string) ([]byte, error) {
	payload, err := os.ReadFile(b.path(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return payload, err
}

func (b *fileStateBackend) Close() error { return nil }
func (b *fileStateBackend) Name() string { return "file" }
