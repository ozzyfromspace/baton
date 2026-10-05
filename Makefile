GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := github.com/ozzyfromspace/baton
# Reproducible: no cgo, no local paths, no VCS stamping; the version is the only input that varies.
FLAGS   := -trimpath -buildvcs=false -ldflags "-s -w -X $(PKG)/internal/version.Version=$(VERSION)"
TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64

SHA256  := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

.PHONY: build test vet e2e cross release-prep verify-release clean

build:
	CGO_ENABLED=0 $(GO) build $(FLAGS) -o dist/baton ./cmd/baton

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Real-claude end-to-end scenarios; each run costs a few cents.
e2e: build
	BATON_E2E=1 $(GO) test ./test/e2e/... -count=1 -v -timeout 30m

cross:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		out=dist/baton_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(FLAGS) -o $$out ./cmd/baton || exit 1; \
	done

# Prepare a release (VERSION=v0.1.0): build every target, pin their sha256 in plugin/checksums.txt (the
# launcher refuses a download that does not match) and set the plugin's version. Commit, then tag.
release-prep:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$$' || { echo "VERSION must look like v1.2.3 or v1.2.3-rc.1"; exit 1; }
	rm -rf dist
	$(MAKE) cross VERSION=$(VERSION)
	cd dist && $(SHA256) baton_$(VERSION)_* > ../plugin/checksums.txt
	sed -i.bak -E 's/"version": "[^"]*"/"version": "$(patsubst v%,%,$(VERSION))"/' plugin/.claude-plugin/plugin.json && rm plugin/.claude-plugin/plugin.json.bak
	@cat plugin/checksums.txt

# Rebuild VERSION and check every binary against plugin/checksums.txt (CI runs this before publishing).
verify-release:
	rm -rf dist
	$(MAKE) cross VERSION=$(VERSION)
	cd dist && $(SHA256) -c ../plugin/checksums.txt

clean:
	rm -rf dist
