# Agent Chat Room - MCP Server

Birden fazla Claude Code agent'inin birbirleriyle haberlesmesinisaglayan MCP sunucusu.

## Ozellikler

- Dinamik agent sayisi (1-8 agent)
- Opsiyonel Yonetici Claude (--manager flag)
- Otomatik sonsuz dongu onleme
- `expects_reply` parametresi ile kontrollu iletisim
- tmux ile dinamik panel yonetimi

## Calisma Modlari

### 1. Dogrudan Mod (Varsayilan)
Agent'lar birbirleriyle dogrudan mesajlasir. Yonetici yok.

```
Backend <---> Mobile <---> Web
```

### 2. Yonetici Modu (--manager)
Tum mesajlar yoneticiye bildirilir, o koordine eder.

```
Backend --> Yonetici --> Mobile
```

## Kurulum

### 1. Repoyu Klonla

```bash
git clone https://github.com/mytsx/agent-chat.git
cd agent-chat
```

### 2. Python Ortamini Kur

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

```bash
claude mcp add agent-chat -- /FULL/PATH/TO/agent-chat/venv/bin/python /FULL/PATH/TO/agent-chat/server.py
```

### 5. Global Erisim (Opsiyonel)

Herhangi bir dizinden `agent-setup` ve `agent-orch` komutlarini kullanmak icin:

```bash
# Symlink olustur
sudo ln -sf /FULL/PATH/TO/agent-chat/setup.py /usr/local/bin/agent-setup
sudo ln -sf /FULL/PATH/TO/agent-chat/orchestrator.py /usr/local/bin/agent-orch
```

Artik herhangi bir yerden:
```bash
agent-setup 3 --names backend,mobile,web
agent-orch --watch
```

## Kullanim Senaryolari

### Senaryo 1: Yonetici Olmadan (2 Agent)

```bash
# 1. Setup
./setup.py 2 --names backend,mobile

# 2. tmux'a baglan
tmux attach -t agents

# 3. Pane 0: Orchestrator
./orchestrator.py --watch

# 4. Pane 1: Backend
claude
# "Sen backend developer'sin. agent-chat'e 'backend' olarak katil."

# 5. Pane 2: Mobile
claude
# "Sen mobile developer'sin. agent-chat'e 'mobile' olarak katil."
```

**Sonuc:** backend <--> mobile dogrudan konusur

### Senaryo 2: Yonetici Ile (3 Agent)

```bash
# 1. Setup
./setup.py 3 --manager --names backend,mobile,web

# 2. tmux'a baglan
tmux attach -t agents

# 3. Pane 0: Orchestrator
./orchestrator.py --watch --manager

# 4. Pane 1: Yonetici
claude
# Yonetici prompt'unu yapistir (docs/MANAGER_PROMPT.md)

# 5. Pane 2-4: Agent'lar
claude
# "Sen [ROL]'sun. agent-chat'e '[ISIM]' olarak katil."
```

**Sonuc:** Tum mesajlar yoneticiye bildirilir, o koordine eder

## Setup Komutlari

```bash
# Temel kullanim
./setup.py AGENT_SAYISI [--manager] [--names isim1,isim2,...]

# Ornekler
./setup.py 2                              # 2 agent (backend, frontend)
./setup.py 3 --names backend,mobile,web   # 3 agent ozel isimlerle
./setup.py 3 --manager                    # 3 agent + yonetici
./setup.py 4 --manager --names a,b,c,d    # 4 agent + yonetici, ozel isimler
```

## Orchestrator Komutlari

```bash
./orchestrator.py --watch              # Dogrudan mod (yonetici yok)
./orchestrator.py --watch --manager    # Yonetici modu
./orchestrator.py --clear              # State temizle
./orchestrator.py --status             # Durum goster
./orchestrator.py --assign backend 2   # Manuel pane atama
```

## MCP Araclari

| Arac | Aciklama |
|------|----------|
| `join_room(agent_name, role)` | Odaya katil |
| `send_message(from_agent, content, to_agent, expects_reply, priority)` | Mesaj gonder |
| `read_messages(agent_name, since_id, limit)` | Sana gelen mesajlari oku (varsayilan limit: 10) |
| `read_all_messages(since_id, limit)` | TUM mesajlari oku (varsayilan limit: 15) |
| `list_agents()` | Odadaki agent'lari listele |
| `leave_room(agent_name)` | Odadan ayril |
| `clear_room()` | Odayi temizle |
| `get_last_message_id()` | Son mesaj ID'sini al |

### send_message Parametreleri

| Parametre | Varsayilan | Aciklama |
|-----------|------------|----------|
| `from_agent` | (zorunlu) | Gonderen agent adi |
| `content` | (zorunlu) | Mesaj icerigi |
| `to_agent` | `"all"` | Hedef agent veya "all" (broadcast) |
| `expects_reply` | `True` | `False` = tesekkur/onay mesaji (bildirim gonderilmez) |
| `priority` | `"normal"` | `"urgent"`, `"normal"`, `"low"` |

## Dosya Yapisi

```
agent-chat/
├── server.py           # MCP sunucusu
├── orchestrator.py     # Mesaj yonlendirici
├── setup.py            # Dinamik tmux setup
├── config/
│   └── base_prompt.txt # Temel MCP tanimi
├── docs/
│   ├── ARCHITECTURE.md
│   └── MANAGER_PROMPT.md
└── README.md
```

## Veri Dosyalari

```
/tmp/agent-chat-room/
├── messages.json           # Mesajlar
├── agents.json             # Aktif agent'lar
├── agent_panes.json        # Agent -> Pane mapping
├── setup_config.json       # Setup konfigurasyonu
└── orchestrator_state.json # Son islenen mesaj ID
```

## Sonsuz Dongu Onleme

Orchestrator su mesajlari otomatik atlar:
- Tesekkur: "tesekkurler", "sagol", "thanks"
- Onay: "tamam", "anladim", "ok", "oldu"
- Olumlu: "super", "harika", "mukemmel"
- Veda: "gorusuruz", "iyi calismalar"

Agent'lar `expects_reply=False` kullanarak da donguyu onleyebilir:
```
send_message("backend", "Tesekkurler!", "mobile", expects_reply=False)
```

## Gereksinimler

- Python 3.10+
- tmux
- Claude Code CLI
- macOS veya Linux

## Lisans

MIT
