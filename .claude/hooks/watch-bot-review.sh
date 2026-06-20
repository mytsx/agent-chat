#!/bin/bash
# Claude Code PostToolUse(Bash) asyncRewake hook'u — BOT BAŞINA izleyici.
# Kullanım: watch-bot-review.sh gemini|codex
#
# git push sonrası, açık PR'daki son "/gemini review" isteğinden SONRA kendi
# botunun review'unu bekler:
#   - Review geldi  → içerik + satır-içi yorumlarla exit 2 (Claude uyanır)
#   - Zaman aşımı   → durum mesajıyla exit 2:
#       codex : "review atlamadı" = bulgu yok kabul edilir (bilgi sinyali)
#       gemini: gelmemesi anormaldir — tetik yorumu/bot kontrol edilmeli
# Böylece her tur iki bağımsız bildirim üretir; Codex'in değişken gecikmesi
# (4-15 dk) Gemini bildirimini geciktirmez.
#
# Yapılandırma (env): GH_REPO, WATCH_TRIES, WATCH_INTERVAL, MARK_DIR
set -u

BOT="${1:-}"
case "$BOT" in
  gemini) LOGIN="gemini-code-assist[bot]";  DEFAULT_TRIES=45 ;;  # ~15 dk
  codex)  LOGIN="chatgpt-codex-connector[bot]"; DEFAULT_TRIES=50 ;;  # ~17 dk
  *) exit 0 ;;
esac

REPO="${GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null)}"
[ -n "$REPO" ] || exit 0
TRIES="${WATCH_TRIES:-$DEFAULT_TRIES}"
INTERVAL="${WATCH_INTERVAL:-20}"

c=$(jq -r '.tool_input.command // ""')
case "$c" in *"git push"*) ;; *) exit 0;; esac

cd "${CLAUDE_PROJECT_DIR:-.}" 2>/dev/null || exit 0
b=$(git branch --show-current 2>/dev/null)
[ -n "$b" ] || exit 0
n=$(gh pr list --repo "$REPO" --head "$b" --state open --json number --jq '.[0].number' 2>/dev/null)
[ -n "$n" ] || exit 0

# Tur referansı: son "/gemini review" isteğinin zamanı (trigger hook'u senkron
# ve bizden önce koştuğu için push sonrası yorum çoktan PR'dadır).
sleep 5
T=$(gh api --paginate "repos/$REPO/issues/$n/comments" \
  --jq '[.[] | select(.body == "/gemini review")] | last | .created_at' 2>/dev/null)
{ [ -n "$T" ] && [ "$T" != "null" ]; } || exit 0

# Çift-bildirim koruması: bot+PR başına son bildirilen tur (T) saklanır —
# ardışık iki push aynı turu iki kez bildirmesin.
MARK="${MARK_DIR:-/tmp}/claude-${BOT}-watch-${REPO//\//-}-pr$n.last"
mkdir -p "$(dirname "$MARK")" 2>/dev/null
if [ "$(cat "$MARK" 2>/dev/null)" = "$T" ]; then
  exit 0
fi

fetch_reviews() {
  gh api --paginate "repos/$REPO/pulls/$n/reviews" --jq \
    "[.[] | select(.submitted_at > \"$T\") | select(.user.login == \"$LOGIN\")]" 2>/dev/null
}

i=0
while [ "$i" -lt "$TRIES" ]; do
  i=$((i + 1))
  sleep "$INTERVAL"
  REVIEWS=$(fetch_reviews)
  COUNT=$(printf '%s' "$REVIEWS" | jq 'length' 2>/dev/null || echo 0)
  [ "${COUNT:-0}" -gt 0 ] || continue

  echo "$T" > "$MARK"
  INLINE=$(gh api --paginate "repos/$REPO/pulls/$n/comments" --jq \
    "[.[] | select(.created_at > \"$T\") | select(.user.login == \"$LOGIN\")] | map({path, line: (.line // .original_line), body})" 2>/dev/null)

  echo "PR #$n ($REPO) — $BOT review'u geldi ('/gemini review' isteği: $T):"
  echo
  echo "## Review gövdesi"
  printf '%s' "$REVIEWS" | jq -r '.[] | "### \(.user.login) [\(.state)] @\(.submitted_at)\n\(.body)\n"'
  echo "## Satır-içi yorumlar"
  printf '%s' "$INLINE" | jq -r '.[] | "- \(.path):\(.line)\n\(.body)\n"'
  echo
  echo "GÖREV: Bulguları değerlendir (adversarial — yanlış-pozitifleri gerekçeli reddet)."
  echo "Geçerli bulguları düzelt; typecheck+lint+build temizse commit+push et (push yeni"
  echo "turu tetikler). Bulgu yoksa bu botun turu temizdir; diğer botun bildirimini de"
  echo "gördüysen ve iki taraf da temizse döngüyü bitirip kullanıcıya özet bildir."
  exit 2
done

# Zaman aşımı: bot bu tura yanıt vermedi.
echo "$T" > "$MARK"
WAITED_MIN=$(( TRIES * INTERVAL / 60 ))
echo "PR #$n ($REPO) — $BOT, '/gemini review' ($T) turuna ~${WAITED_MIN} dk içinde review GÖNDERMEDİ."
if [ "$BOT" = "codex" ]; then
  echo "Codex push'ları kendi tetikler ve bulgu bulamadığında çoğu kez review ATLAR —"
  echo "bu tur Codex açısından TEMİZ kabul edilebilir. Gemini bildirimiyle birlikte değerlendir."
else
  echo "UYARI: Gemini '/gemini review' yorumuna normalde birkaç dakikada yanıt verir —"
  echo "gelmemesi anormal. Tetik yorumunu/bot durumunu kontrol et (kota, sunset, erişim)."
fi
exit 2
