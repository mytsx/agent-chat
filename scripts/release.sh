#!/usr/bin/env bash
set -euo pipefail

# Tek komutla sürüm çıkarma: build + imza + notarize + GitHub Release + Homebrew tap bump.
# İmza anahtarı bu makinede kalır; hiçbir GitHub secret'ı gerekmez.
#
# Kullanım:
#   scripts/release.sh <version>       # örn: scripts/release.sh 0.7.0   ('v' öneki YOK)
#
# Ön koşullar: gh + wails kurulu, keychain'de "Developer ID Application" kimliği,
# notarytool profili "AC_PASSWORD" saklı, yerel main = origin/main.

REPO="mytsx/agent-chat"
TAP="mytsx/homebrew-agent-chat"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." >/dev/null && pwd)"
cd "$ROOT"

die() { echo "HATA: $*" >&2; exit 1; }

VERSION="${1:-}"
[ -n "$VERSION" ] || die "kullanım: scripts/release.sh <version>   (örn 0.7.0)"
case "$VERSION" in v*) die "sürümü 'v' öneki OLMADAN ver (örn 0.7.0, v0.7.0 değil)";; esac
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "sürüm X.Y.Z biçiminde olmalı (aldım: $VERSION)"

TAG="v$VERSION"
DMG="dist/AgentChat-${VERSION}-universal.dmg"

command -v gh    >/dev/null || die "gh (GitHub CLI) gerekli"
command -v wails >/dev/null || die "wails CLI gerekli"

# İmza kimliğini keychain'den oku (tek makineye gömülü değil, taşınabilir)
DEVELOPER_ID="$(security find-identity -v -p codesigning 2>/dev/null \
  | grep -o 'Developer ID Application: [^"]*' | head -1)"
[ -n "$DEVELOPER_ID" ] || die "keychain'de 'Developer ID Application' imza kimliği bulunamadı"
echo "==> İmza kimliği: $DEVELOPER_ID"

# Aynı sürüm iki kez yayınlanmasın
if gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1; then
  die "$TAG zaten yayınlanmış — sürümü artır ya da önce mevcut release'i sil"
fi

# Tag uzak main'i işaret edecek: yerel HEAD ile origin/main aynı olmalı
git fetch -q origin main || die "git fetch başarısız"
LOCAL_MAIN="$(git rev-parse HEAD)"
REMOTE_MAIN="$(git rev-parse origin/main)"
[ "$LOCAL_MAIN" = "$REMOTE_MAIN" ] || \
  die "yerel HEAD ile origin/main farklı — önce push/pull et (release uzak main'i tag'ler)"

# --- 1) Build + imza + notarize (senin saklı kimlik bilgilerinle) ---
echo "==> Build + imza + notarize (make release) — birkaç dakika, iki notary round-trip..."
make release VERSION="$VERSION" DEVELOPER_ID="$DEVELOPER_ID"
[ -f "$DMG" ] || die "DMG üretilemedi: $DMG"

echo "==> Notarization doğrulanıyor..."
xcrun stapler validate "$DMG" >/dev/null 2>&1 || die "DMG staple doğrulaması başarısız"
spctl -a -t open --context context:primary-signature "$DMG" >/dev/null 2>&1 \
  || die "DMG Gatekeeper doğrulaması başarısız"
echo "==> DMG imzalı + notarize + staple: $DMG"

# --- 2) GitHub Release (DMG asset ile) ---
echo "==> GitHub Release oluşturuluyor: $TAG"
gh release create "$TAG" \
  --repo "$REPO" \
  --target "$REMOTE_MAIN" \
  --title "Agent Chat $TAG" \
  --generate-notes \
  "$DMG"

# --- 3) Homebrew tap bump ---
# render-cask.sh, DMG'yi yayınlanan release URL'sinden indirip sha256'yı kendisi hesaplar
# (yayınlanan byte'ları tarif eder + URL'nin çözüldüğünü kanıtlar).
echo "==> Homebrew tap güncelleniyor: $TAP -> $VERSION"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
gh repo clone "$TAP" "$WORK/tap" -- -q
mkdir -p "$WORK/tap/Casks"
"$ROOT/packaging/homebrew/render-cask.sh" "$VERSION" > "$WORK/tap/Casks/agent-chat.rb"
git -C "$WORK/tap" add Casks/agent-chat.rb
if git -C "$WORK/tap" diff --cached --quiet; then
  echo "==> Tap zaten $VERSION — değişiklik yok"
else
  git -C "$WORK/tap" -c user.name="$(git config user.name)" \
      -c user.email="$(git config user.email)" commit -q -m "agent-chat $VERSION"
  git -C "$WORK/tap" push -q
  echo "==> Tap push edildi"
fi

echo ""
echo "✅ $TAG yayınlandı ve Homebrew tap güncellendi."
echo "   Kurulum : brew install --cask $TAP/agent-chat"
echo "   Güncelle: brew upgrade --cask agent-chat"
