# Static, pure-Go builds (modernc SQLite needs no cgo). Override the
# pre-configured API for dev builds: make build API_URL=http://localhost:3010
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
API_URL ?= https://api.storage.bd
PKG     := storage-bd-client/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).DefaultAPIURL=$(API_URL)

.PHONY: build build-all test vet clean release restic
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd ./cmd/storagebd

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd-linux-amd64 ./cmd/storagebd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd-linux-arm64 ./cmd/storagebd

test:
	go test ./...

vet:
	go vet ./...

# Pinned restic (packaging/restic.lock) into dist/restic/, checksum-verified.
restic:
	scripts/fetch-restic.sh amd64 arm64

# Release tarballs + SHA256SUMS in dist/: make release VERSION=v0.1.0
release:
	scripts/build-release.sh $(VERSION) $(API_URL)

clean:
	rm -rf bin dist/stage
