BINARY := biewer
PKG := ./cmd/biewer
DIST := dist
VERSION := 0.1.0-mvp

.PHONY: build build-all test vet fmt clean install

build:
	go build -o $(DIST)/$(BINARY) $(PKG)

# Cross-compiles for Biewer's native-mode targets. CGO is disabled so this
# works from any host with no C cross-toolchain (Biewer has zero cgo
# dependencies by design).
build-all:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BINARY)-darwin-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -o $(DIST)/$(BINARY)-darwin-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o $(DIST)/$(BINARY)-linux-amd64  $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -o $(DIST)/$(BINARY)-linux-arm64  $(PKG)

test:
	go test ./...

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

clean:
	rm -rf $(DIST)
