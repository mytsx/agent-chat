#!/bin/bash
# Agent Chat - 4 Pane Başlatma
# Pane 0: Orchestrator
# Pane 1: Yönetici Claude
# Pane 2: Backend Claude
# Pane 3: Frontend/Mobil Claude

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SESSION="agents"

echo "🚀 Agent Chat Session (4 Pane) başlatılıyor..."

# Mevcut session varsa kapat
tmux kill-session -t $SESSION 2>/dev/null

# Yeni session oluştur
tmux new-session -d -s $SESSION -n chat

# 4 pane oluştur (2x2 grid)
tmux split-window -h -t $SESSION:0
tmux split-window -v -t $SESSION:0.0
tmux split-window -v -t $SESSION:0.1

# Pane 0 - Orchestrator
tmux send-keys -t $SESSION:0.0 "cd $SCRIPT_DIR && clear" C-m
tmux send-keys -t $SESSION:0.0 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.0 "echo '      🎯 ORCHESTRATOR (Pane 0)         '" C-m
tmux send-keys -t $SESSION:0.0 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.0 "echo ''" C-m
tmux send-keys -t $SESSION:0.0 "echo 'Komutlar:'" C-m
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --clear'" C-m
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --assign yonetici 1'" C-m
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --assign backend 2'" C-m
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --assign frontend 3'" C-m
tmux send-keys -t $SESSION:0.0 "echo '  ./orchestrator.py --watch'" C-m

# Pane 1 - Yönetici Claude
tmux send-keys -t $SESSION:0.1 "clear" C-m
tmux send-keys -t $SESSION:0.1 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.1 "echo '      👔 YÖNETİCİ CLAUDE (Pane 1)      '" C-m
tmux send-keys -t $SESSION:0.1 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.1 "echo ''" C-m
tmux send-keys -t $SESSION:0.1 "echo '1. claude'" C-m
tmux send-keys -t $SESSION:0.1 "echo '2. Yönetici prompt yapıştır'" C-m
tmux send-keys -t $SESSION:0.1 "echo '   (docs/MANAGER_PROMPT.md)'" C-m

# Pane 2 - Backend Claude
tmux send-keys -t $SESSION:0.2 "clear" C-m
tmux send-keys -t $SESSION:0.2 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.2 "echo '      🔧 BACKEND CLAUDE (Pane 2)       '" C-m
tmux send-keys -t $SESSION:0.2 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.2 "echo ''" C-m
tmux send-keys -t $SESSION:0.2 "echo '1. cd /backend/proje'" C-m
tmux send-keys -t $SESSION:0.2 "echo '2. claude'" C-m
tmux send-keys -t $SESSION:0.2 "echo '3. \"backend olarak odaya katıl\"'" C-m

# Pane 3 - Frontend Claude
tmux send-keys -t $SESSION:0.3 "clear" C-m
tmux send-keys -t $SESSION:0.3 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.3 "echo '      🎨 FRONTEND CLAUDE (Pane 3)      '" C-m
tmux send-keys -t $SESSION:0.3 "echo '═══════════════════════════════════════'" C-m
tmux send-keys -t $SESSION:0.3 "echo ''" C-m
tmux send-keys -t $SESSION:0.3 "echo '1. cd /frontend/proje'" C-m
tmux send-keys -t $SESSION:0.3 "echo '2. claude'" C-m
tmux send-keys -t $SESSION:0.3 "echo '3. \"frontend olarak odaya katıl\"'" C-m

# Mouse desteği
tmux set-option -t $SESSION mouse on

echo ""
echo "✅ Session hazır!"
echo ""
echo "┌──────────────┬──────────────┐"
echo "│  Orchestrator│  Yönetici    │"
echo "│   (Pane 0)   │   (Pane 1)   │"
echo "├──────────────┼──────────────┤"
echo "│  Backend     │  Frontend    │"
echo "│   (Pane 2)   │   (Pane 3)   │"
echo "└──────────────┴──────────────┘"
echo ""
echo "Şimdi çalıştır:"
echo "  tmux attach -t agents"
echo ""
