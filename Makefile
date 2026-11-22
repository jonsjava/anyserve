GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/jonsjava/anyserve/internal/cli.Version=$(VERSION)
KEY     ?= $(HOME)/.config/anyserve-dev/registry-signing.key

PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

.PHONY: build test test-full check registry release clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/anyserve ./cmd/anyserve

# Quick tests (skips the slow supervisor kill test).
test:
	$(GO) vet ./...
	$(GO) test -short ./...

test-full:
	$(GO) vet ./...
	$(GO) test -race ./...

# Validate every service definition without signing anything.
check:
	$(GO) run ./tools/regbuild -check -src registry/services

# Rebuild the signed registry snapshot embedded in the binary.
registry:
	$(GO) run ./tools/regbuild -src registry/services -out internal/registry/snapshot -key $(KEY)

# Cross-compile release binaries into dist/.
release:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		out=dist/anyserve-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/anyserve || exit 1; \
	done
	@cd dist && sha256sum anyserve-$(VERSION)-* > SHA256SUMS-$(VERSION)

clean:
	rm -rf bin dist
