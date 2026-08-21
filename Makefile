BINARY := biewer
PKG := ./cmd/biewer
DIST := dist
VERSION := 0.1.0-mvp

.PHONY: build build-rocksdb build-all dist-plan dist-snapshot test test-rocksdb vet fmt clean install install-rocksdb

build:
	go build -o $(DIST)/$(BINARY) $(PKG)

# Production build with the native RocksDB dashboard store. Requires the
# RocksDB C library (`brew install rocksdb` on macOS, `librocksdb-dev` on
# Debian/Ubuntu) and pkg-config.
build-rocksdb:
	CGO_ENABLED=1 go build -tags rocksdb -o $(DIST)/$(BINARY) $(PKG)

# Cross-compiles for Biewer's native-mode targets. CGO is disabled so this
# works from any host with no C cross-toolchain (Biewer has zero cgo
# dependencies by design).
build-all:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BINARY)-darwin-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -o $(DIST)/$(BINARY)-darwin-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o $(DIST)/$(BINARY)-linux-amd64  $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o $(DIST)/$(BINARY)-linux-arm64  $(PKG)

# Preview or build the same archives/installers produced by release CI.
dist-plan:
	dist plan

dist-snapshot:
	dist build --artifacts=all

test:
	go test ./...

test-rocksdb:
	CGO_ENABLED=1 go test -tags rocksdb ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Builds for the current host and installs to ~/.local/bin (create it and
# add it to PATH if it isn't already).
install: build
	mkdir -p $(HOME)/.local/bin
	cp $(DIST)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "Installed to $(HOME)/.local/bin/$(BINARY)"

install-rocksdb: build-rocksdb
	mkdir -p $(HOME)/.local/bin
	cp $(DIST)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo "Installed RocksDB build to $(HOME)/.local/bin/$(BINARY)"

clean:
	rm -rf $(DIST)
