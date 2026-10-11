VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: build install test release clean

build:
	go build -ldflags '$(LDFLAGS)' -o den ./cmd/den

install:
	mkdir -p $(PREFIX)/bin
	go build -ldflags '$(LDFLAGS)' -o $(PREFIX)/bin/den ./cmd/den

test:
	go vet ./... && XDG_STATE_HOME=$$(mktemp -d) go test ./...

# Static linux binaries (Den needs Hyprland, so Linux only), plus checksums.
# Builds into dist/ only; nothing is published from here.
release: clean
	mkdir -p dist
	for arch in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/den_linux_$$arch ./cmd/den || exit 1; \
	done
	cd dist && sha256sum den_* > SHA256SUMS
	@ls -l dist

clean:
	rm -rf dist den
