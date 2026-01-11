# Agent Chat Room - MCP Server

Birden fazla Claude Code agent'ının birbirleriyle haberleşmesini sağlayan MCP sunucusu. **Yönetici Claude** tüm iletişimi koordine eder.

## 4 Panel Yapısı

```
┌──────────────┬──────────────┐
│  Orchestrator│  Yönetici    │
│   (Pane 0)   │   Claude     │
│   Python     │   (Pane 1)   │
├──────────────┼──────────────┤
│  Backend     │  Frontend    │
│   Claude     │   Claude     │
│   (Pane 2)   │   (Pane 3)   │
└──────────────┴──────────────┘
```

## Nasıl Çalışır?

```
Backend → Mesaj → Orchestrator → Yönetici Claude → Analiz → Karar → Talimat → Frontend
```

1. **Backend** bir mesaj gönderir
2. **Python Orchestrator** yeni mesajı tespit eder
3. **Yönetici Claude**'a bildirir
4. **Yönetici** mesajı analiz eder, karar verir:
   - Soru mu? → İlgili agent'a "cevap ver" talimatı
   - Bilgi mi? → "Bilgin olsun" bildirimi
   - Teşekkür/veda mı? → Kimseye bildirme (sonsuz döngü önleme!)
5. **Yönetici** ilgili agent'a talimat gönderir

## Kurulum

### 1. Repoyu Klonla

```bash
git clone https://github.com/kullanici/agent-chat.git
cd agent-chat
```

### 2. Python Ortamını Kur

```bash
python3 -m venv venv
source venv/bin/activate
pip install -r requirements.txt
```

### 3. tmux Kur (macOS)

```bash
brew install tmux
```

### 4. Claude Code'a MCP Ekle

`~/.claude/claude_code_config.json`:

```json
{
  "mcpServers": {
    "agent-chat": {
      "command": "/FULL/PATH/TO/agent-chat/venv/bin/python",
      "args": ["/FULL/PATH/TO/agent-chat/server.py"]
    }
  }
}
```

## Hızlı Başlangıç

### 1. tmux Session Başlat

```bash
./start.sh
tmux attach -t agents
```

### 2. Pane 0 (Orchestrator)

```bash
./orchestrator.py --clear
./orchestrator.py --assign yonetici 1
./orchestrator.py --assign backend 2
./orchestrator.py --assign frontend 3
./orchestrator.py --watch
```

### 3. Pane 1 (Yönetici Claude)

```bash
claude
```

Sonra `docs/MANAGER_PROMPT.md` içeriğini yapıştır.

### 4. Pane 2 (Backend Claude)

```bash
cd /backend/proje
claude
```

Sonra: `backend olarak agent chat odasına katıl`

### 5. Pane 3 (Frontend Claude)

```bash
cd /frontend/proje
claude
```

Sonra: `frontend olarak agent chat odasına katıl`

## MCP Araçları

| Araç | Açıklama |
|------|----------|
| `join_room(agent_name, role)` | Odaya katıl |
| `send_message(from, content, to)` | Mesaj gönder |
| `read_messages(agent_name)` | Mesajları oku |
| `list_agents()` | Agent'ları listele |
| `leave_room(agent_name)` | Odadan ayrıl |

## Dosya Yapısı

```
agent-chat/
├── server.py           # MCP sunucusu
├── orchestrator.py     # Python orchestrator (4 pane)
├── start.sh            # tmux başlatıcı
├── requirements.txt    # Bağımlılıklar
├── README.md           # Bu dosya
└── docs/
    ├── ARCHITECTURE.md # Mimari detayları
    └── MANAGER_PROMPT.md # Yönetici prompt'u
```

## Veri Dosyaları

```
/tmp/agent-chat-room/
├── messages.json       # Mesajlar
├── agents.json         # Agent'lar
├── agent_panes.json    # Pane mapping
└── orchestrator_state.json
```

## Gereksinimler

- Python 3.10+
- tmux
- Claude Code CLI
- macOS veya Linux

## Lisans

MIT
