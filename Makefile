SHELL := /bin/sh

BINARY  := yt
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

MODULE  := github.com/ArasanDev/yt
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: all build install test fmt vet check snapshot release-check clean

all: build

## build: compile a static binary into ./bin/yt
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/yt

## install: install into GOBIN (or GOPATH/bin) with version metadata
install:
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/yt

## test: run the full suite with the race detector
test:
	$(GO) test -race ./...

## fmt: format all Go code in place
fmt:
	gofmt -w .

## vet: run the standard static checks
vet:
	$(GO) vet ./...

## check: what CI runs
check: fmt vet test

## snapshot: build release archives locally without publishing
snapshot:
	goreleaser release --snapshot --clean

## release-check: validate .goreleaser.yaml
release-check:
	goreleaser check

clean:
	rm -rf bin dist
