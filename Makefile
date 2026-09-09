GO      ?= go
GORELEASER ?= goreleaser
PKG     := github.com/marcus/comms
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(PKG)/pkg/buildinfo.Version=$(VERSION) \
           -X $(PKG)/pkg/buildinfo.Commit=$(COMMIT)
BIN     := bin
PREFIX  ?= $(HOME)/.local
BREW_PREFIX ?= $(shell brew --prefix 2>/dev/null)

.PHONY: all build install install-local use-homebrew uninstall test cover lint fmt fmt-check tidy vet check clean \
	release-snapshot release-verify release release-dry-run release-tap release-check-state

all: check

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN)/comms ./cmd/comms

install: build
	@mkdir -p '$(PREFIX)/bin'
	install -m 0755 '$(BIN)/comms' '$(PREFIX)/bin/comms'
	@echo "installed comms -> $(PREFIX)/bin/comms"

# install-local puts this checkout's build where the Homebrew comms lives, so
# the `comms` already on PATH is the dev build and no shell configuration
# changes. It unlinks the formula first so Homebrew does not fight the file;
# use-homebrew puts the released binary back.
install-local: build
	@test -n '$(BREW_PREFIX)' || { echo "Homebrew not found; use 'make install' and put $(PREFIX)/bin on PATH"; exit 1; }
	@brew unlink comms >/dev/null 2>&1 || true
	install -m 0755 '$(BIN)/comms' '$(BREW_PREFIX)/bin/comms'
	@echo "installed dev comms -> $(BREW_PREFIX)/bin/comms"
	@echo "restart the daemon with 'comms restart'; restore the release with 'make use-homebrew'"

use-homebrew:
	@test -n '$(BREW_PREFIX)' || { echo "Homebrew not found"; exit 1; }
	rm -f '$(BREW_PREFIX)/bin/comms'
	brew link --overwrite comms
	@echo "restored the Homebrew comms; restart the daemon with 'comms restart'"

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

fmt-check:
	@unformatted="$$(gofmt -s -l .)"; \
		test -z "$$unformatted" || { echo "files need gofmt -s:"; echo "$$unformatted"; exit 1; }

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

check: fmt-check build test vet lint

release-snapshot:
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean

release-verify: release-snapshot
	./scripts/release-verify-assets.sh dist
	./scripts/release-test.sh dist

release-check-state:
	./scripts/release-check-state.sh pre-tag

release:
	./scripts/release.sh

release-dry-run:
	./scripts/release.sh --dry-run

release-tap:
	./scripts/release-tap.sh

clean:
	rm -rf $(BIN) dist coverage.out
