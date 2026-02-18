#!/bin/bash
# Notification hook: Claude input beklerken macOS bildirimi gönder

LOG="/tmp/claude-hooks.log"
log() { echo "[$(date '+%H:%M:%S')] [notify-done] $*" >> "$LOG"; }

log "Hook tetiklendi"
osascript -e 'display notification "Claude Code cevabını tamamladı" with title "Claude Code" sound name "Ping"' 2>/dev/null && \
  log "BASARILI: Bildirim gonderildi" || \
  log "HATA: Bildirim gonderilemedi"

exit 0
