#!/usr/bin/env bash
set -euo pipefail

# Code signing script for Agent Chat
# Signs the .app bundle with Developer ID certificate + hardened runtime

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist/universal"
ENTITLEMENTS="$PROJECT_ROOT/build/darwin/entitlements.plist"

APP_NAME="Agent Chat"
APP_PATH="$DIST_DIR/${APP_NAME}.app"

# Developer ID — override via environment variable
DEVELOPER_ID="${DEVELOPER_ID:-Developer ID Application: Mehmet Yerli (VTVG4G3NFH)}"

if [ ! -d "$APP_PATH" ]; then
    echo "ERROR: App bundle not found at $APP_PATH"
    echo "Run 'make build-universal' first."
    exit 1
fi

if [ ! -f "$ENTITLEMENTS" ]; then
    echo "ERROR: Entitlements file not found at $ENTITLEMENTS"
    exit 1
fi

echo "==> Signing ${APP_NAME}.app with: $DEVELOPER_ID"

# Remove any existing signatures
echo "==> Removing existing signatures..."
codesign --remove-signature "$APP_PATH" 2>/dev/null || true

# Sign nested frameworks and dylibs first (depth-first)
echo "==> Signing nested components..."
find "$APP_PATH/Contents/Frameworks" -type f \( -name "*.dylib" -o -name "*.framework" \) 2>/dev/null | while read -r item; do
    codesign --force --sign "$DEVELOPER_ID" \
        --options runtime \
        --timestamp \
        "$item"
done

# Sign the main executable
echo "==> Signing main executable..."
codesign --force --sign "$DEVELOPER_ID" \
    --options runtime \
    --timestamp \
    --entitlements "$ENTITLEMENTS" \
    "$APP_PATH/Contents/MacOS/AgentChat"

# Sign the entire bundle
echo "==> Signing app bundle..."
codesign --force --sign "$DEVELOPER_ID" \
    --options runtime \
    --timestamp \
    --entitlements "$ENTITLEMENTS" \
    "$APP_PATH"

# Verify
echo "==> Verifying signature..."
codesign --verify --deep --strict --verbose=2 "$APP_PATH"

echo "==> Signature verification passed"
echo "==> Signed: $APP_PATH"
