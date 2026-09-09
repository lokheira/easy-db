# easy-db - Makefile (build & verify entry for the teaching database)
#
# Usage (on Windows, if `make` is not on PATH, use `mingw32-make` instead):
#   make build     build cmd/easydb -> ./easydb.exe
#   make vet       run static checks
#   make fmt       format sources with gofmt (-w)
#   make fmt-check verify formatting (fails if any file is not gofmt-ed)
#   make test      run unit tests
#   make test-short run tests with -short
#   make test-race run tests with the race detector
#   make repl      build and start the interactive REPL
#   make run       DEV: rebuild only when sources changed, then start REPL
#   make clean     remove build artifacts
#
# Note: targets are declared .PHONY to avoid clashes with same-named files;
# the default target is `all`.

GO      ?= go
GOFMT   ?= gofmt
OUT     ?= easydb.exe
PKG     ?= ./...

SRCS    := $(shell find ./cmd ./internal -name '*.go' 2>/dev/null)
BUILDDIR:= .build
BIN     := $(BUILDDIR)/$(OUT)

.PHONY: all build vet fmt fmt-check test test-short test-race repl run clean

all: fmt-check vet test build

build:
	$(GO) build $(GOFLAGS) -o $(OUT) ./cmd/easydb
	@echo "build ok: $(OUT)"

# DEV target: compile only if sources are newer than the cached binary,
# then launch it. Change OUT to reuse this flow with another name.
$(BIN): $(SRCS)
	@mkdir -p $(BUILDDIR)
	$(GO) build $(GOFLAGS) -o $(BIN) ./cmd/easydb

run: $(BIN)
	./$(BIN)

vet:
	$(GO) vet $(PKG)
	@echo "vet ok"

fmt:
	$(GOFMT) -w $(shell find . -name '*.go' -not -path './vendor/*')
	@echo "fmt ok"

fmt-check:
	@out="$$(gofmt -l . | grep -v '^vendor/' || true)"; \
	if [ -n "$$out" ]; then \
		echo "files not gofmt-ed (run 'make fmt' first):"; \
		echo "$$out"; \
		exit 1; \
	fi
	@echo "fmt ok"

test:
	$(GO) test $(PKG)
	@echo "test ok"

test-short:
	$(GO) test -short $(PKG)

test-race:
	$(GO) test -race $(PKG)

repl: build
	./$(OUT)

clean:
	rm -rf $(BUILDDIR)
	rm -f $(OUT)
	@echo "clean ok"
