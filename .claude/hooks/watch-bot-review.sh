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

# Read the payload ONCE: it arrives on stdin, so a second jq would see nothing.
PAYLOAD=$(cat)
c=$(printf '%s' "$PAYLOAD" | jq -r '.tool_input.command // ""' 2>/dev/null || echo "")
case "$c" in *"git push"*) ;; *) exit 0;; esac

# NOT gated on the command's exit status. The matcher fires on any compound
# command CONTAINING a push, and the status belongs to the whole command:
# `git push && make test` reports failure although a review round started, and
# `git push || echo failed` reports success although none did. The round is
# instead evidenced by the PR head itself (below) — if the push did not land,
# the head is unchanged and this run is a no-op.

cd "${CLAUDE_PROJECT_DIR:-.}" 2>/dev/null || exit 0
b=$(git branch --show-current 2>/dev/null)
[ -n "$b" ] || exit 0
# PR henüz açılmamış olabilir (ilk push PR'dan önce gelir) — kısa süre bekle.
# Boş sonuç bu gh sürümünde boş çıktı veriyor (ölçüldü), ama "null" basan
# sürümlere karşı ikisi de eleniyor: "null" boş olmadığı için döngüyü kırar ve
# izleyici tüm zaman aşımı boyunca pulls/null'u yoklardı.
n=""
for _ in 1 2 3 4 5 6; do
  n=$(gh pr list --repo "$REPO" --head "$b" --state open --json number --jq '.[0].number' 2>/dev/null)
  case "$n" in ""|null) n="" ;; *) break ;; esac
  sleep 10
done
[ -n "$n" ] || exit 0

# Tur SHA'sı PR'ın uzak head'inden alınıyor, lokal HEAD'den değil: `git push`
# rastgele refspec kabul ediyor (`git push origin HEAD~1:dal`, yalnız tag
# push'u, ya da push'tan sonra tekrar commit'leyen bileşik komut), o durumlarda
# lokal HEAD PR'a giden commit DEĞİL — izleyici yanlış SHA'yı yoklar ve
# işaretiyle o commit'in gerçek push'unu bastırırdı. Bu push PR head'ini hiç
# değiştirmediyse SHA öncekiyle aynı kalır, çift-bildirim koruması devreye girer
# ve hook doğru biçimde hiçbir şey yapmaz.
SHA=""
for _ in 1 2 3 4 5 6; do
  SHA=$(gh pr view "$n" --repo "$REPO" --json headRefOid --jq '.headRefOid' 2>/dev/null)
  case "$SHA" in ""|null) SHA="" ;; *) break ;; esac
  sleep 5
done
[ -n "$SHA" ] || exit 0

# Round bookkeeping, two files with different lifetimes:
#   .last — the head SHA of the last COMPLETED round. Written only after this
#           run has produced its notification, so a watcher that is cancelled or
#           killed at its process timeout does not permanently record a SHA
#           whose review nobody ever surfaced; the next push of that commit then
#           still starts a watcher.
#   .lock — this run, while it polls. It keeps a second watcher off the same
#           round without outliving the process that holds it.
MARK="${MARK_DIR:-/tmp}/claude-${BOT}-watch-${REPO//\//-}-pr$n.last"
# Per-ROUND lock, so a watcher still polling an older SHA never blocks the round
# a newer push just started.
LOCK="$MARK.$SHA.lock"
mkdir -p "$(dirname "$MARK")" 2>/dev/null
if [ "$(cat "$MARK" 2>/dev/null)" = "$SHA" ]; then
  exit 0
fi

# mkdir IS the acquisition: it is atomic, so two hooks racing for the same round
# cannot both win and emit duplicate wake-ups — a read-then-write pair can.
# A lock left behind by a force-killed watcher (its EXIT trap never ran) is
# reclaimed by age; otherwise that SHA would be blocked forever.
lock_age() {
  now=$(date +%s)
  mtime=$(stat -f %m "$LOCK" 2>/dev/null || stat -c %Y "$LOCK" 2>/dev/null || echo "$now")
  echo $(( now - mtime ))
}
if ! mkdir "$LOCK" 2>/dev/null; then
  STALE_AFTER=$(( TRIES * INTERVAL + 300 ))
  if [ "$(lock_age)" -le "$STALE_AFTER" ]; then
    exit 0
  fi
  rm -rf "$LOCK" 2>/dev/null
  mkdir "$LOCK" 2>/dev/null || exit 0
fi
trap 'rm -rf "$LOCK"' EXIT

# Zaman penceresi. Bu hook, komutun TAMAMI bittikten sonra çalışıyor
# (PostToolUse), yani `git push && <uzun görev>` biçiminde bir komutta bot
# review'unu biz daha başlamadan göndermiş olabilir. Bu yüzden pay dar değil:
# 30 dk geriye bakılıyor. Asıl anahtar zaten commit — pencere, yalnızca soğuk
# başlangıçta (işaret dosyası yokken) keyfi eski bir review'un bu turun sonucu
# sanılmasını engelliyor.
LOOKBACK="${WATCH_LOOKBACK_SECONDS:-1800}"
T=$(date -u -v-"${LOOKBACK}"S +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -d "$LOOKBACK seconds ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u +%Y-%m-%dT%H:%M:%SZ)

# Reviews are matched by the COMMIT they reviewed, not by "newer than $T": in a
# rapid review-fix-push cycle the previous round's review can land inside the
# lookback window, and a time filter would report it as this push's review while
# the real one never surfaces.
#
# --paginate emits one JSON document PER PAGE, so the pages are streamed as bare
# objects and slurped into a single array here; piping a per-page "[...]" into
# `jq length` yields one integer per line and breaks the numeric test below.
# Commit AND time. The commit alone is not enough on a cold start: with no
# marker yet, a tag-only or already-up-to-date push while a PR is open records
# the PR's existing head, and an arbitrarily old review for that commit would be
# surfaced as this push's result. Requiring the review to be newer than the
# window makes that case time out — which is correct, since such a push starts
# no review round. (A PENDING review carries submitted_at null; jq orders null
# below any string, so it is excluded here, as it should be.)
fetch_reviews() {
  gh api --paginate "repos/$REPO/pulls/$n/reviews" --jq \
    ".[] | select(.commit_id == \"$SHA\") | select(.submitted_at > \"$T\") | select(.user.login == \"$LOGIN\")" 2>/dev/null \
    | jq -s '.'
}

i=0
while [ "$i" -lt "$TRIES" ]; do
  i=$((i + 1))
  sleep "$INTERVAL"
  REVIEWS=$(fetch_reviews)
  COUNT=$(printf '%s' "$REVIEWS" | jq 'length' 2>/dev/null || echo 0)
  [ "${COUNT:-0}" -gt 0 ] || continue

  # Inline comments are taken from the matched reviews by review id — exact —
  # with a login+window fallback, because Copilot posts its inline comments under
  # a DIFFERENT login than its review and may not link them to the review id.
  REVIEW_IDS=$(printf '%s' "$REVIEWS" | jq '[.[].id]')
  # The fallback is bound to the pushed commit as well: a login+window match
  # alone would accept a comment from the previous round (created inside the
  # lookback) or from a newer push whose review id is not in the set, and the
  # wake-up would present findings for the wrong revision.
  #
  # original_commit_id, NOT commit_id. Measured on this very PR: GitHub
  # re-anchors an inline comment's commit_id to the current head, so four
  # comments written against 2c56a13 all reported commit_id 33ad355 once that
  # was pushed — matching on it would accept exactly the stale round this filter
  # exists to exclude. original_commit_id keeps the commit the comment was
  # actually written against. (Review-level commit_id is NOT re-anchored, so the
  # review query above is fine.)
  # The comments request is retried and its FAILURE is kept: piping a failed
  # request straight into jq turns it into an empty list, and the watcher would
  # record the round as done while silently dropping every inline finding.
  RAW=""
  INLINE_OK=0
  for _ in 1 2 3; do
    if RAW=$(gh api --paginate "repos/$REPO/pulls/$n/comments" --jq '.[]' 2>/dev/null); then
      INLINE_OK=1
      break
    fi
    sleep 5
  done
  INLINE=$(printf '%s' "$RAW" \
    | jq -s --argjson ids "$REVIEW_IDS" --arg login "$INLINE_LOGIN" --arg since "$T" --arg sha "$SHA" \
      '[.[] | select((.pull_request_review_id as $r | $ids | index($r))
                     or (.user.login == $login and .created_at > $since
                         and .original_commit_id == $sha))]
       | unique_by(.id) | map({path, line: (.line // .original_line), body})')

  echo "$SHA" > "$MARK"
  echo "PR #$n ($REPO) — $BOT review'u geldi (push: $SHA, pencere: $T sonrası):"
  echo
  echo "## Review gövdesi"
  printf '%s' "$REVIEWS" | jq -r '.[] | "### \(.user.login) [\(.state)] @\(.submitted_at)\n\(.body)\n"'
  echo "## Satır-içi yorumlar"
  if [ "$INLINE_OK" != "1" ]; then
    echo "UYARI: satır-içi yorumlar ÇEKİLEMEDİ (API hatası, 3 deneme). Liste eksik;"
    echo "turu kapatmadan önce PR'ı elle kontrol et."
  fi
  printf '%s' "$INLINE" | jq -r '.[] | "- \(.path):\(.line)\n\(.body)\n"'
  echo
  echo "GÖREV: Bulguları değerlendir (adversarial — yanlış-pozitifleri gerekçeli reddet)."
  echo "Geçerli bulguları düzelt; typecheck+lint+build temizse commit+push et (push yeni"
  echo "turu tetikler). Bulgu yoksa bu botun turu temizdir; diğer botun bildirimini de"
  echo "gördüysen ve iki taraf da temizse döngüyü bitirip kullanıcıya özet bildir."
  exit 2
done

# Zaman aşımı: bot bu tura yanıt vermedi. Tur tamamlandı sayılır.
echo "$SHA" > "$MARK"
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
