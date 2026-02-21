#!/usr/bin/env bash
set -euo pipefail

# Universal binary build script for Agent Chat
# Produces a universal (arm64 + amd64) .app bundle

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist/universal"
BUILD_DIR="$PROJECT_ROOT/build"

VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE"

APP_NAME="Agent Chat"
OUTPUT_FILENAME="AgentChat"

echo "==> Building Agent Chat $VERSION ($COMMIT) universal binary"

# Clean previous builds
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"
mkdir -p "$BUILD_DIR"

# --- Step 1: Build universal MCP server binary ---
echo "==> Building MCP server (arm64)..."
GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$BUILD_DIR/mcp-server-bin-arm64" ./cmd/mcp-server

echo "==> Building MCP server (amd64)..."
GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$BUILD_DIR/mcp-server-bin-amd64" ./cmd/mcp-server

echo "==> Creating universal MCP server binary..."
lipo -create -output "$BUILD_DIR/mcp-server-bin" \
    "$BUILD_DIR/mcp-server-bin-arm64" \
    "$BUILD_DIR/mcp-server-bin-amd64"

# Clean arch-specific binaries
rm -f "$BUILD_DIR/mcp-server-bin-arm64" "$BUILD_DIR/mcp-server-bin-amd64"

# --- Step 2: Build frontend ---
echo "==> Building frontend..."
cd "$PROJECT_ROOT/frontend"
npm install --silent
npm run build
cd "$PROJECT_ROOT"

# --- Step 3: Build Wails app for both architectures ---
# The universal MCP binary is already at build/mcp-server-bin,
# so go:embed will pick it up for both arch builds.

echo "==> Building app (arm64)..."
GOOS=darwin GOARCH=arm64 wails build -platform darwin/arm64 -ldflags "$LDFLAGS" -skipbindings -s
mv "$BUILD_DIR/bin/${APP_NAME}.app" "$BUILD_DIR/bin/${APP_NAME}-arm64.app"

echo "==> Building app (amd64)..."
GOOS=darwin GOARCH=amd64 wails build -platform darwin/amd64 -ldflags "$LDFLAGS" -skipbindings -s
mv "$BUILD_DIR/bin/${APP_NAME}.app" "$BUILD_DIR/bin/${APP_NAME}-amd64.app"

# --- Step 4: Create universal app bundle ---
echo "==> Creating universal app bundle..."
cp -R "$BUILD_DIR/bin/${APP_NAME}-arm64.app" "$DIST_DIR/${APP_NAME}.app"

lipo -create \
    "$BUILD_DIR/bin/${APP_NAME}-arm64.app/Contents/MacOS/${OUTPUT_FILENAME}" \
    "$BUILD_DIR/bin/${APP_NAME}-amd64.app/Contents/MacOS/${OUTPUT_FILENAME}" \
    -output "$DIST_DIR/${APP_NAME}.app/Contents/MacOS/${OUTPUT_FILENAME}"

# Clean arch-specific app bundles
rm -rf "$BUILD_DIR/bin/${APP_NAME}-arm64.app" "$BUILD_DIR/bin/${APP_NAME}-amd64.app"

# Verify
echo "==> Verifying universal binary..."
file "$DIST_DIR/${APP_NAME}.app/Contents/MacOS/${OUTPUT_FILENAME}"

echo "==> Build complete: $DIST_DIR/${APP_NAME}.app"
