GO      ?= go
GORELEASER ?= goreleaser
PKG     := github.com/marcus/comms
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(PKG)/pkg/buildinfo.Version=$(VERSION) \
           -X $(PKG)/pkg/buildinfo.Commit=$(COMMIT)
BIN     := bin
PREFIX  ?= $(HOME)/.local

.PHONY: all build install uninstall test cover lint fmt tidy vet check clean release-snapshot

all: check

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN)/comms ./cmd/comms

install: build
	@mkdir -p '$(PREFIX)/bin'
	install -m 0755 '$(BIN)/comms' '$(PREFIX)/bin/comms'
	@echo "installed comms -> $(PREFIX)/bin/comms"

uninstall:
	rm -f '$(PREFIX)/bin/comms'

test:
	$(GO) test -race -count=1 ./...

cover:
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run

fmt:
	$(GO) fmt ./...
	gofmt -s -w .

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

check: build test vet lint

release-snapshot:
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean

clean:
	rm -rf $(BIN) dist coverage.out
