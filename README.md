# Agent Chat Room - MCP Server

Birden fazla Claude Code agent'ının birbirleriyle haberleşmesini sağlayan MCP sunucusu ve orchestrator sistemi.

```
┌─────────────────────────────────────────────────────┐
│                   Orchestrator                       │
│    (Mesajları izler, agent'lara bildirim gönderir)  │
└─────────────────────┬───────────────────────────────┘
                      │
        ┌─────────────┴─────────────┐
        ▼                           ▼
┌───────────────┐           ┌───────────────┐
│   Claude A    │◄─────────►│   Claude B    │
│  (Backend)    │   MCP     │  (Frontend)   │
└───────────────┘           └───────────────┘
```

## Özellikler

- **MCP Tabanlı Mesajlaşma**: Agent'lar MCP tool'ları ile mesaj gönderir/alır
- **Orchestrator**: Yeni mesaj geldiğinde ilgili agent'a otomatik bildirim
- **tmux Entegrasyonu**: Tek pencerede çoklu terminal yönetimi
- **Akıllı Zamanlama**: Agent meşgulken mesaj göndermez, bekler

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

`~/.claude/claude_code_config.json` dosyasına ekle:

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

> ⚠️ `/FULL/PATH/TO/` kısmını kendi dizininizle değiştirin!

## Hızlı Başlangıç

### 1. tmux Session Başlat

```bash
./start.sh
tmux attach -t agents
```

3 pane yan yana göreceksin. Mouse ile tıklayarak pane değiştirebilirsin.

### 2. Agent'ları Başlat

**Pane 1 (Orta):**
```bash
cd /proje/backend
claude
```
Claude'a de: `backend olarak agent chat odasına katıl`

**Pane 2 (Sağ):**
```bash
cd /proje/frontend
claude
```
Claude'a de: `frontend olarak agent chat odasına katıl`

### 3. Orchestrator'ı Başlat

**Pane 0 (Sol):**
```bash
./orchestrator.py --clear
./orchestrator.py --assign backend 1
./orchestrator.py --assign frontend 2
./orchestrator.py --watch
```

### 4. Test Et!

Pane 1'de (backend) Claude'a de:
> "frontend'e mesaj gönder: API hazır, endpoint'ler aktif"

Orchestrator otomatik olarak frontend'i bilgilendirecek!

## MCP Araçları

| Araç | Açıklama |
|------|----------|
| `join_room(agent_name, role)` | Odaya katıl |
| `send_message(from_agent, content, to_agent)` | Mesaj gönder ("all" = herkese) |
| `read_messages(agent_name, since_id)` | Mesajları oku |
| `list_agents()` | Odadaki agent'ları listele |
| `leave_room(agent_name)` | Odadan ayrıl |
| `clear_room()` | Tüm mesaj ve kayıtları sil |
| `get_last_message_id()` | Son mesaj ID'sini al |

## Orchestrator Komutları

```bash
./orchestrator.py --setup      # tmux session kur
./orchestrator.py --clear      # State'i temizle
./orchestrator.py --assign NAME PANE  # Agent'ı pane'e ata
./orchestrator.py --watch      # Mesajları izle ve bildir
./orchestrator.py --status     # Durum göster
```

## Nasıl Çalışır?

1. **MCP Server** (`server.py`):
   - `/tmp/agent-chat-room/` dizininde JSON dosyaları tutar
   - `messages.json`: Tüm mesajlar
   - `agents.json`: Aktif agent'lar

2. **Orchestrator** (`orchestrator.py`):
   - Mesaj dosyasını izler
   - Yeni mesaj geldiğinde hedef pane'e `tmux send-keys` ile bildirim gönderir
   - Agent meşgulse (spinner görünüyorsa) bekler

3. **tmux**:
   - Tek pencerede 3 pane: orchestrator + 2 claude instance
   - Mouse ile pane boyutu ayarlanabilir

## Dosya Yapısı

```
agent-chat/
├── server.py           # MCP sunucusu
├── orchestrator.py     # Terminal yöneticisi
├── start.sh            # tmux hızlı başlatma
├── requirements.txt    # Python bağımlılıkları
└── README.md
```

## Gereksinimler

- Python 3.10+
- tmux
- Claude Code CLI
- macOS veya Linux

## Lisans

MIT
