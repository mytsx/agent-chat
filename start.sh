#!/bin/bash
# Agent Chat - Hızlı Başlatma
# Bu script tmux session'ı kurar ve talimatları gösterir

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SESSION="agents"

echo "🚀 Agent Chat Session başlatılıyor..."

# Mevcut session varsa kapat
tmux kill-session -t $SESSION 2>/dev/null

# Yeni session - 3 pane yan yana
tmux new-session -d -s $SESSION -n chat

# Pane'leri oluştur
tmux split-window -h -t $SESSION:0
tmux split-window -h -t $SESSION:0

# Layout düzenle
tmux select-layout -t $SESSION:0 even-horizontal

# Pane 0'a orchestrator dizinine git
tmux send-keys -t $SESSION:0.0 "cd $SCRIPT_DIR && clear" Enter
tmux send-keys -t $SESSION:0.0 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.0 "echo '       🎯 ORCHESTRATOR PANE (0)            '" Enter
tmux send-keys -t $SESSION:0.0 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.0 "echo ''" Enter
tmux send-keys -t $SESSION:0.0 "echo 'Komutlar:'" Enter
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --clear    # Temizle'" Enter
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --assign backend 1'" Enter
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --assign frontend 2'" Enter
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --watch    # Başlat'" Enter
tmux send-keys -t $SESSION:0.0 "echo ''" Enter

# Pane 1'e bilgi
tmux send-keys -t $SESSION:0.1 "clear" Enter
tmux send-keys -t $SESSION:0.1 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.1 "echo '       🔧 BACKEND AGENT PANE (1)           '" Enter
tmux send-keys -t $SESSION:0.1 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.1 "echo ''" Enter
tmux send-keys -t $SESSION:0.1 "echo 'Buraya claude code başlat:'" Enter
tmux send-keys -t $SESSION:0.1 "echo '  cd /your/backend/project'" Enter
tmux send-keys -t $SESSION:0.1 "echo '  claude'" Enter
tmux send-keys -t $SESSION:0.1 "echo ''" Enter
tmux send-keys -t $SESSION:0.1 "echo 'Sonra: backend olarak agent chat odasına katıl'" Enter
tmux send-keys -t $SESSION:0.1 "echo ''" Enter

# Pane 2'ye bilgi
tmux send-keys -t $SESSION:0.2 "clear" Enter
tmux send-keys -t $SESSION:0.2 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.2 "echo '       🎨 FRONTEND AGENT PANE (2)          '" Enter
tmux send-keys -t $SESSION:0.2 "echo '═══════════════════════════════════════════'" Enter
tmux send-keys -t $SESSION:0.2 "echo ''" Enter
tmux send-keys -t $SESSION:0.2 "echo 'Buraya claude code başlat:'" Enter
tmux send-keys -t $SESSION:0.2 "echo '  cd /your/frontend/project'" Enter
tmux send-keys -t $SESSION:0.2 "echo '  claude'" Enter
tmux send-keys -t $SESSION:0.2 "echo ''" Enter
tmux send-keys -t $SESSION:0.2 "echo 'Sonra: frontend olarak agent chat odasına katıl'" Enter
tmux send-keys -t $SESSION:0.2 "echo ''" Enter

echo ""
echo "✅ Session hazır!"
echo ""
echo "Şimdi şunu çalıştır:"
echo "  tmux attach -t agents"
echo ""
