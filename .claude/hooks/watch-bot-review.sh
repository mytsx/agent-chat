#!/bin/bash
# Claude Code PostToolUse(Bash) asyncRewake hook'u — BOT BAŞINA izleyici.
# Kullanım: watch-bot-review.sh codex|copilot
#
# git push sonrası, itilen commit'e gelen bot review'unu bekler:
#   - Review geldi  → içerik + satır-içi yorumlarla exit 2 (Claude uyanır)
#   - Zaman aşımı   → durum mesajıyla exit 2 (codex bulgu yoksa review ATLAR,
#                     yani zaman aşımı o bot için "temiz" demektir)
# Her bot bağımsız bildirim üretir; birinin gecikmesi diğerini bekletmez.
#
# Tur referansı push'un KENDİSİ (head SHA + hook'un başlangıç zamanı). Daha önce
# "/gemini review" yorumunun zaman damgasıydı; Gemini sunset olup o tetikleyici
# kalkınca Codex izleme de sessizce çalışmayı bırakırdı (#105).
#
# Yapılandırma (env): GH_REPO, WATCH_TRIES, WATCH_INTERVAL, MARK_DIR
set -u

BOT="${1:-}"
case "$BOT" in
  codex)
    LOGIN="chatgpt-codex-connector[bot]"
    # Satır-içi yorumları da aynı bot hesabından gelir.
    INLINE_LOGIN="$LOGIN"
    DEFAULT_TRIES=50 ;;  # ~17 dk
  copilot)
    # Review-seviyesi ile satır-içi yorumlar FARKLI login kullanır; ikisini tek
    # isimle aramak satır-içi bulguları görünmez yapar.
    LOGIN="copilot-pull-request-reviewer[bot]"
    INLINE_LOGIN="Copilot"
    DEFAULT_TRIES=45 ;;  # ~15 dk
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
SHA=$(git rev-parse HEAD 2>/dev/null)
[ -n "$SHA" ] || exit 0

# PR henüz açılmamış olabilir (ilk push PR'dan önce gelir) — kısa süre bekle.
n=""
for _ in 1 2 3 4 5 6; do
  n=$(gh pr list --repo "$REPO" --head "$b" --state open --json number --jq '.[0].number' 2>/dev/null)
  [ -n "$n" ] && break
  sleep 10
done
[ -n "$n" ] || exit 0

# Çift-bildirim koruması: bot+PR başına son izlenen head SHA saklanır — aynı
# commit'i iki kez iten bir tur (retry, --force-with-lease) iki bildirim üretmesin.
MARK="${MARK_DIR:-/tmp}/claude-${BOT}-watch-${REPO//\//-}-pr$n.last"
mkdir -p "$(dirname "$MARK")" 2>/dev/null
if [ "$(cat "$MARK" 2>/dev/null)" = "$SHA" ]; then
  exit 0
fi
# Beklemeden ÖNCE işaretle: aynı SHA için ikinci bir izleyici başlamasın.
echo "$SHA" > "$MARK"

# Zaman penceresi: push anından geriye 2 dk pay. Bot'un review'u push'tan sonra
# gelir; pay, saat kaymasına ve hook'un birkaç saniyelik gecikmesine karşıdır.
T=$(date -u -v-120S +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -d '120 seconds ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u +%Y-%m-%dT%H:%M:%SZ)

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

  INLINE=$(gh api --paginate "repos/$REPO/pulls/$n/comments" --jq \
    "[.[] | select(.created_at > \"$T\") | select(.user.login == \"$INLINE_LOGIN\")] | map({path, line: (.line // .original_line), body})" 2>/dev/null)

  echo "PR #$n ($REPO) — $BOT review'u geldi (push: $SHA, pencere: $T sonrası):"
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
WAITED_MIN=$(( TRIES * INTERVAL / 60 ))
echo "PR #$n ($REPO) — $BOT, $SHA push'una ~${WAITED_MIN} dk içinde review GÖNDERMEDİ."
if [ "$BOT" = "codex" ]; then
  echo "Codex push'ları kendi tetikler ve bulgu bulamadığında çoğu kez review ATLAR —"
  echo "bu tur Codex açısından TEMİZ kabul edilebilir."
else
  echo "Copilot seçicidir ve her turda review yazmayabilir; bu tur Copilot açısından"
  echo "temiz kabul edilebilir. Bot durumunu (erişim/kota) yalnızca üst üste birkaç"
  echo "turda hiç yanıt gelmiyorsa kontrol et."
fi
echo "NOT: Bu bir zaman aşımı bildirimidir, botun 'temiz' dediğinin KANITI DEĞİL."
echo "PR'ın kendi review durumunu (gh pr view) doğrulamadan turu kapatma."
exit 2
