GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := github.com/ozzyfromspace/baton
# Reproducible: no cgo, no local paths, no VCS stamping; the version is the only input that varies.
FLAGS   := -trimpath -buildvcs=false -ldflags "-s -w -X $(PKG)/internal/version.Version=$(VERSION)"
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64

.PHONY: build test vet e2e cross clean

build:
	CGO_ENABLED=0 $(GO) build $(FLAGS) -o dist/baton ./cmd/baton

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Real-claude end-to-end scenarios; each run costs a few cents.
e2e: build
	BATON_E2E=1 $(GO) test ./test/e2e/... -count=1 -v

cross:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		out=dist/baton_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(FLAGS) -o $$out ./cmd/baton || exit 1; \
	done

clean:
	rm -rf dist
