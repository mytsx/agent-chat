#!/usr/bin/env bash
set -euo pipefail

# DMG creation script for Agent Chat
# Creates a distributable DMG with app + Applications symlink

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist/universal"

APP_NAME="Agent Chat"
APP_PATH="$DIST_DIR/${APP_NAME}.app"
VERSION="${VERSION:-dev}"
DMG_NAME="AgentChat-${VERSION}-universal"
DMG_PATH="$PROJECT_ROOT/dist/${DMG_NAME}.dmg"
STAGING_DIR="$DIST_DIR/dmg-staging"

# Optional: sign and notarize the DMG too
DEVELOPER_ID="${DEVELOPER_ID:-}"
KEYCHAIN_PROFILE="${KEYCHAIN_PROFILE:-AC_PASSWORD}"

if [ ! -d "$APP_PATH" ]; then
    echo "ERROR: App bundle not found at $APP_PATH"
    echo "Run 'make build-universal' first."
    exit 1
fi

# Clean previous DMG
rm -f "$DMG_PATH"
rm -rf "$STAGING_DIR"

# Create staging directory
echo "==> Creating DMG staging area..."
mkdir -p "$STAGING_DIR"
cp -R "$APP_PATH" "$STAGING_DIR/"
ln -s /Applications "$STAGING_DIR/Applications"

# Create DMG
echo "==> Creating DMG..."
hdiutil create -volname "$APP_NAME" \
    -srcfolder "$STAGING_DIR" \
    -ov -format UDBZ \
    "$DMG_PATH"

# Clean staging
rm -rf "$STAGING_DIR"

# Sign DMG if DEVELOPER_ID is set
if [ -n "$DEVELOPER_ID" ]; then
    echo "==> Signing DMG..."
    codesign --force --sign "$DEVELOPER_ID" --timestamp "$DMG_PATH"

    # Notarize DMG
    echo "==> Notarizing DMG..."
    xcrun notarytool submit "$DMG_PATH" \
        --keychain-profile "$KEYCHAIN_PROFILE" \
        --wait

    echo "==> Stapling DMG..."
    xcrun stapler staple "$DMG_PATH"
fi

echo "==> DMG created: $DMG_PATH"
ls -lh "$DMG_PATH"
