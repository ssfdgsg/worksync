BINARY   := worksync
GO       ?= go
PKG      := ./cmd/worksync

GOFILES  := $(shell find . -name '*.go' -not -path './.gocache/*' -not -path './.gopath/*' -not -path './.modcache/*')

.PHONY: all build test vet fmt fmt-check lint install clean

all: build

build:
	$(GO) build -o bin/$(BINARY) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@out=$$(gofmt -l $(GOFILES)); \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

lint: vet fmt-check

install:
	$(GO) install $(PKG)

clean:
	rm -rf bin dist
