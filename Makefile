.PHONY: build dev clean mcp-server build-universal sign notarize dmg release build-unsigned

VERSION ?= 0.1.0
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

export VERSION COMMIT BUILD_DATE

# Version metadata injected into the desktop binary (mirrors scripts/build-universal.sh).
# A plain `make build` uses VERSION=0.1.0, which version.IsDevBuild treats as a dev
# build (no update check); `make build VERSION=x.y.z` produces a real versioned artifact.
GO_LDFLAGS = -X main.buildVersion=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

mcp-server:
	go build -o build/mcp-server-bin ./cmd/mcp-server

build: mcp-server
	wails build -ldflags "$(GO_LDFLAGS)"

dev: mcp-server
	wails dev

clean:
	rm -f build/mcp-server-bin
	rm -rf dist/

# --- Release Pipeline ---

build-universal:
	./scripts/build-universal.sh

sign:
	./scripts/sign.sh

notarize:
	./scripts/notarize.sh

dmg:
	./scripts/create-dmg.sh

release: build-universal sign notarize dmg
	@echo "==> Release complete: dist/AgentChat-$(VERSION)-universal.dmg"

# Unsigned DMG for local testing (no certificate needed)
build-unsigned: build-universal
	VERSION=$(VERSION) ./scripts/create-dmg.sh
