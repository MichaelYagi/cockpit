GO       ?= go
PKG      := ./...
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/MichaelYagi/cockpit/internal/buildinfo.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 windows/amd64

export CGO_ENABLED ?= 0

.PHONY: all build fmt fmt-check vet lint test test-race test-integration cross vuln examples check dist clean

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

# Release archives for Linux (D26): make dist VERSION=v0.4.0 writes
# dist/cockpit_<version>_linux_<arch>.tar.gz (binary, deploy/, docs,
# README, CHANGELOG) and dist/SHA256SUMS.
DIST_ARCHES := amd64 arm64
dist:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || { echo "VERSION must look like v1.2.3 (got $(VERSION))"; exit 1; }
	rm -rf dist && mkdir -p dist
	@for arch in $(DIST_ARCHES); do \
		name=cockpit_$(VERSION)_linux_$$arch; stage=dist/$$name; \
		mkdir -p $$stage && \
		GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $$stage/cockpit ./cmd/cockpit && \
		cp -R deploy docs README.md CHANGELOG.md $$stage/ && rm -f $$stage/deploy/*.go && \
		tar -C dist --owner=0 --group=0 --sort=name -czf dist/$$name.tar.gz $$name && rm -rf $$stage || exit 1; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS && cat SHA256SUMS

clean:
	rm -rf bin dist
