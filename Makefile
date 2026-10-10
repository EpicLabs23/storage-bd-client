# Static, pure-Go builds (modernc SQLite needs no cgo). Override the
# pre-configured API for dev builds: make build API_URL=http://localhost:3000
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
API_URL ?= https://api.storage.bd
PKG     := storage-bd-client/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).DefaultAPIURL=$(API_URL)

.PHONY: build build-all test vet clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd ./cmd/storagebd

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd-linux-amd64 ./cmd/storagebd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/storagebd-linux-arm64 ./cmd/storagebd

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
