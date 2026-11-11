GO       ?= go
PKG      := ./...
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/MichaelYagi/cockpit/internal/buildinfo.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 windows/amd64

export CGO_ENABLED ?= 0

.PHONY: all build fmt fmt-check vet lint test test-race test-integration cross vuln examples check clean

all: check build

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/cockpit ./cmd/cockpit

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKG)

lint:
	$(GO) tool staticcheck $(PKG)

test:
	$(GO) test $(PKG)

# The race detector requires cgo.
test-race:
	CGO_ENABLED=1 $(GO) test -race $(PKG)

test-integration:
	$(GO) test -tags integration $(PKG)

cross:
	@for p in $(PLATFORMS); do \
		echo "building $$p"; \
		GOOS=$${p%/*} GOARCH=$${p#*/} $(GO) build -o /dev/null ./... || exit 1; \
		GOOS=$${p%/*} GOARCH=$${p#*/} $(GO) vet ./... || exit 1; \
	done

vuln:
	$(GO) tool govulncheck $(PKG)

# The example extension is a separate module (examples/collector) that
# builds against this checkout.
examples:
	cd examples/collector && $(GO) vet ./... && $(GO) test ./... && $(GO) build -o /dev/null .

check: fmt-check vet lint test-race cross examples

clean:
	rm -rf bin
