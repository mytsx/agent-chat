#!/usr/bin/env bash
set -euo pipefail

# Renders the Homebrew cask for a published Agent Chat release.
#
# Usage:
#   render-cask.sh <version> [sha256]
#
# <version> is the release version without the leading "v" (e.g. 0.5.0).
# When [sha256] is omitted the DMG is downloaded from the exact URL the cask
# points at and hashed, so the checksum always describes the bytes users get.
#
# The rendered cask is written to stdout; progress goes to stderr.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd)"
TEMPLATE="${TEMPLATE:-$SCRIPT_DIR/agent-chat.rb.tmpl}"
REPO="${REPO:-mytsx/agent-chat}"

die() {
    echo "ERROR: $*" >&2
    exit 1
}

hash_file() {
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        sha256sum "$1" | cut -d' ' -f1
    fi
}

version="${1:-}"
sha256="${2:-}"

[ -n "$version" ] || die "usage: render-cask.sh <version> [sha256]"
[ -f "$TEMPLATE" ] || die "template not found: $TEMPLATE"

# Guard the most likely caller mistake: passing the tag (v0.5.0) instead of the
# version (0.5.0) would render a cask whose URL 404s.
case "$version" in
    v*) die "version must not include the leading 'v' (got: $version)" ;;
esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] ||
    die "version must look like 1.2.3 (got: $version)"

dmg_url="https://github.com/${REPO}/releases/download/v${version}/AgentChat-${version}-universal.dmg"

if [ -z "$sha256" ]; then
    tmp="$(mktemp -t agent-chat-dmg.XXXXXX)"
    trap 'rm -f "$tmp"' EXIT
    echo "==> Downloading $dmg_url" >&2
    curl -fSL --retry 5 --retry-delay 5 --retry-all-errors -o "$tmp" "$dmg_url" ||
        die "could not download $dmg_url (is the release published?)"
    sha256="$(hash_file "$tmp")"
    echo "==> sha256: $sha256" >&2
fi

[[ "$sha256" =~ ^[0-9a-f]{64}$ ]] || die "sha256 must be 64 lowercase hex chars (got: $sha256)"

rendered="$(sed -e "s|__VERSION__|${version}|g" -e "s|__SHA256__|${sha256}|g" "$TEMPLATE")"

# Catch any leftover __PLACEHOLDER__, not just the two substituted above: a
# renamed or newly added placeholder in the template would otherwise ship a cask
# carrying the literal to the tap.
if printf '%s' "$rendered" | grep -Eq '__[A-Z0-9_]+__'; then
    die "unsubstituted placeholder left in rendered cask (template: $TEMPLATE)"
fi

printf '%s\n' "$rendered"
