#!/usr/bin/env bash
set -euo pipefail

# Notarization script for Agent Chat
# Submits the signed .app to Apple for notarization and staples the ticket
#
# Prerequisites:
#   xcrun notarytool store-credentials "AC_PASSWORD" \
#       --apple-id "your@apple-id.com" --team-id "VTVG4G3NFH" --password "app-specific-password"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$PROJECT_ROOT/dist/universal"

APP_NAME="Agent Chat"
APP_PATH="$DIST_DIR/${APP_NAME}.app"
ZIP_PATH="$DIST_DIR/${APP_NAME}.zip"

# Keychain profile name for notarytool credentials
KEYCHAIN_PROFILE="${KEYCHAIN_PROFILE:-AC_PASSWORD}"

if [ ! -d "$APP_PATH" ]; then
    echo "ERROR: App bundle not found at $APP_PATH"
    echo "Run 'make build-universal' and 'make sign' first."
    exit 1
fi

# Verify the app is signed before submitting
echo "==> Verifying code signature before notarization..."
codesign --verify --deep --strict "$APP_PATH" || {
    echo "ERROR: App is not properly signed. Run 'make sign' first."
    exit 1
}

# Create zip for submission
echo "==> Creating zip for notarization submission..."
ditto -c -k --keepParent "$APP_PATH" "$ZIP_PATH"

# Submit for notarization
echo "==> Submitting to Apple for notarization..."
echo "    (This typically takes 2-15 minutes)"
xcrun notarytool submit "$ZIP_PATH" \
    --keychain-profile "$KEYCHAIN_PROFILE" \
    --wait

# Clean up zip
rm -f "$ZIP_PATH"

# Staple the notarization ticket
echo "==> Stapling notarization ticket..."
xcrun stapler staple "$APP_PATH"

# Verify with Gatekeeper
echo "==> Verifying with Gatekeeper..."
spctl --assess --type execute --verbose=2 "$APP_PATH"

echo "==> Notarization complete: $APP_PATH"
