#!/bin/bash
# PostToolUse hook: After a successful git push, comments "/gemini review"
# on the open PR for the current branch (if one exists).
# Requires: gh CLI authenticated.
# Exit codes: 0 = always (non-blocking, informational only).
set -uo pipefail

LOG="/tmp/claude-hooks.log"
log() { echo "[$(date '+%H:%M:%S')] [gemini-review] $*" >> "$LOG"; }

INPUT=$(cat)
log "Hook tetiklendi"

# tool_input.command contains the bash command
CMD=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || echo "")
log "CMD=$CMD"

# Only act on git push commands
if ! echo "$CMD" | grep -qE 'git\s+push'; then
  log "git push degil, cikis"
  exit 0
fi

# Check if push was successful (Claude Code uses tool_response, not tool_output)
TOOL_EXIT=$(echo "$INPUT" | jq -r '.tool_response.exit_code // .tool_response.exitCode // "0"' 2>/dev/null || echo "0")
log "TOOL_EXIT=$TOOL_EXIT"
if [ "$TOOL_EXIT" != "0" ]; then
  log "Push basarisiz, cikis"
  exit 0
fi

# Atomic lock: mkdir atomiktir, paralel çalışan ikinci instance başarısız olur
LOCK_DIR="/tmp/gemini-review-push.lock"
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  # Lock zaten var — eski mi kontrol et (60s üzeri ise stale)
  LOCK_AGE=$(( $(date +%s) - $(stat -f %m "$LOCK_DIR" 2>/dev/null || echo 0) ))
  if [ "$LOCK_AGE" -lt 60 ]; then
    log "ATLA: Paralel calisma engellendi (lock age: ${LOCK_AGE}s)"
    exit 0
  fi
  # Stale lock, temizle ve yeniden al
  rmdir "$LOCK_DIR" 2>/dev/null
  mkdir "$LOCK_DIR" 2>/dev/null || { log "ATLA: Lock alinamadi"; exit 0; }
fi
# Lock temizliği: script bittiğinde lock'u serbest bırak
trap 'rmdir "$LOCK_DIR" 2>/dev/null' EXIT

# Detect GitHub repo from push URLs (origin fetch URL might be GitLab)
REPO=$(git remote get-url --push --all origin 2>/dev/null | grep -o 'github\.com[:/][^.]*' | sed 's|github\.com[:/]||' | head -1)
log "REPO=$REPO"
if [ -z "$REPO" ]; then
  log "GitHub repo bulunamadi, cikis"
  exit 0
fi

# Get current branch
BRANCH=$(git branch --show-current 2>/dev/null)
log "BRANCH=$BRANCH"
if [ -z "$BRANCH" ] || [ "$BRANCH" = "main" ] || [ "$BRANCH" = "master" ] || [ "$BRANCH" = "development" ]; then
  log "Korunan branch, cikis"
  exit 0
fi

# Find open PR for this branch
PR_NUMBER=$(gh pr list --repo "$REPO" --head "$BRANCH" --state open --json number --jq '.[0].number' 2>/dev/null)
log "PR_NUMBER=$PR_NUMBER"
if [ -z "$PR_NUMBER" ] || [ "$PR_NUMBER" = "null" ]; then
  log "Acik PR bulunamadi, cikis"
  exit 0
fi

# Comment /gemini review
gh pr comment "$PR_NUMBER" --repo "$REPO" --body "/gemini review" 2>/dev/null && \
  log "BASARILI: /gemini review → PR #$PR_NUMBER ($REPO)" || \
  log "HATA: gh pr comment basarisiz"

exit 0
