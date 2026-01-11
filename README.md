# Agent Chat Room - MCP Server

Birden fazla Claude Code agent'ının birbirleriyle haberleşmesini sağlayan MCP sunucusu. **Yönetici Claude** tüm iletişimi koordine eder, **akıllı analiz** sonsuz döngüleri önler.

## Özellikler

- ✅ Yönetici Claude ile merkezi koordinasyon
- ✅ Otomatik sonsuz döngü önleme (teşekkür/onay mesajları atlanır)
- ✅ `expects_reply` parametresi ile kontrollü iletişim
- ✅ tmux ile 4 panel yönetimi

## 4 Panel Yapısı

```
┌──────────────┬──────────────┐
│ Orchestrator │  Yönetici    │
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
Backend → Mesaj → Orchestrator → Analiz → Yönetici Claude → Karar → Frontend
```

1. **Backend** bir mesaj gönderir
2. **Orchestrator** mesajı analiz eder:
   - Teşekkür/onay mı? → `skip` (sonsuz döngü önleme!)
   - Değilse → Yönetici'ye bildir
3. **Yönetici Claude** mesajı değerlendirir ve ilgili agent'a talimat gönderir
4. **Frontend** talimatı alır ve cevap verir

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

**Yöntem 1 - CLI ile (önerilen):**
```bash
claude mcp add agent-chat -- /FULL/PATH/TO/agent-chat/venv/bin/python /FULL/PATH/TO/agent-chat/server.py
```

**Yöntem 2 - Manuel:**
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

Sonra `docs/MANAGER_PROMPT.md` içeriğindeki prompt'u yapıştır.

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
| `send_message(from, content, to, expects_reply, priority)` | Mesaj gönder |
| `read_messages(agent_name)` | Sana gelen mesajları oku |
| `read_all_messages()` | TÜM mesajları oku (admin) |
| `list_agents()` | Agent'ları listele |
| `leave_room(agent_name)` | Odadan ayrıl |
| `clear_room()` | Odayı temizle |
| `get_last_message_id()` | Son mesaj ID'sini al |

### send_message Parametreleri

| Parametre | Varsayılan | Açıklama |
|-----------|------------|----------|
| `expects_reply` | `True` | `False` ise teşekkür/onay mesajı - bildirim gönderilmez |
| `priority` | `"normal"` | `"urgent"`, `"normal"`, `"low"` |

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
