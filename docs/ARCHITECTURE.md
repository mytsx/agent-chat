# Agent Chat Room - Mimari Dokümantasyonu

## Genel Bakış

Bu sistem, birden fazla Claude Code agent'ının birbirleriyle haberleşmesini sağlar. Bir **Yönetici Claude** tüm iletişimi koordine eder.

## 4 Panel Yapısı

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              tmux session: agents                            │
├────────────────┬────────────────┬────────────────┬────────────────────────────┤
│    Pane 0      │    Pane 1      │    Pane 2      │         Pane 3            │
│                │                │                │                           │
│   Python       │   Yönetici     │   Backend      │   Frontend/Mobil          │
│  Orchestrator  │    Claude      │    Claude      │      Claude               │
│                │                │                │                           │
│  - Mesajları   │  - Analiz      │  - Backend     │   - Frontend/Mobil        │
│    izler       │  - Karar       │    projesi     │     projesi               │
│  - Yöneticiye  │  - Yönlendir   │    üzerinde    │     üzerinde              │
│    bildirir    │                │    çalışır     │     çalışır               │
│                │                │                │                           │
└────────────────┴────────────────┴────────────────┴────────────────────────────┘
```

## Bileşenler

### 1. MCP Server (`server.py`)
Agent'ların mesajlaşması için araçlar sağlar:
- `join_room(agent_name, role)` - Odaya katıl
- `send_message(from, content, to)` - Mesaj gönder
- `read_messages(agent_name)` - Mesajları oku
- `list_agents()` - Agent'ları listele

### 2. Python Orchestrator (`orchestrator.py`)
Arka planda çalışır:
- `/tmp/agent-chat-room/messages.json` dosyasını izler
- Yeni mesaj geldiğinde Yönetici Claude'a bildirir
- tmux send-keys ile pane'lere komut gönderir

### 3. Yönetici Claude (Pane 1)
İletişimi koordine eden akıllı agent:
- Tüm mesajları analiz eder
- Kimin cevap vermesi gerektiğine karar verir
- Sonsuz döngüleri önler (teşekkür → rica ederim → ...)
- Gerektiğinde özetler

### 4. Çalışan Agent'lar (Pane 2, 3, ...)
Asıl işi yapan agent'lar:
- Kendi projelerinde çalışırlar
- Yönetici'den talimat alırlar
- Birbirleriyle mesajlaşırlar

## Mesaj Akışı

```
┌─────────────────────────────────────────────────────────────────┐
│                         MESAJ AKIŞI                              │
└─────────────────────────────────────────────────────────────────┘

  Backend          Orchestrator        Yönetici           Frontend
    │                   │                 │                   │
    │ 1. send_message   │                 │                   │
    │ "Soru var?"       │                 │                   │
    │──────────────────►│                 │                   │
    │                   │                 │                   │
    │                   │ 2. Yeni mesaj!  │                   │
    │                   │ "Kontrol et"    │                   │
    │                   │────────────────►│                   │
    │                   │                 │                   │
    │                   │           3. Mesajı okur            │
    │                   │              analiz eder            │
    │                   │                 │                   │
    │                   │           4. Karar verir:           │
    │                   │           "Frontend cevap           │
    │                   │            vermeli"                 │
    │                   │                 │                   │
    │                   │                 │ 5. Talimat        │
    │                   │                 │ "Cevap ver"       │
    │                   │                 │──────────────────►│
    │                   │                 │                   │
    │                   │                 │    6. Cevap       │
    │                   │                 │◄──────────────────│
    │                   │                 │                   │
    │                   │           7. Analiz eder            │
    │                   │                 │                   │
    │  8. "Cevap geldi" │                 │                   │
    │◄──────────────────│◄────────────────│                   │
    │                   │                 │                   │
```

## Yönetici Claude Karar Mantığı

```
┌─────────────────────────────────────────────────────────────────┐
│                    YÖNETİCİ KARAR AĞACI                         │
└─────────────────────────────────────────────────────────────────┘

                         Yeni Mesaj
                             │
                             ▼
                    ┌─────────────────┐
                    │ Mesaj içeriğini │
                    │   analiz et     │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
         ┌────────┐    ┌──────────┐   ┌──────────┐
         │ Soru?  │    │  Bilgi?  │   │Teşekkür/ │
         │        │    │          │   │  Veda?   │
         └───┬────┘    └────┬─────┘   └────┬─────┘
             │              │              │
             ▼              ▼              ▼
      ┌────────────┐ ┌────────────┐ ┌────────────┐
      │ İlgili     │ │ Bilgilendir│ │   SKIP     │
      │ agent'a    │ │ ama cevap  │ │ Bildirim   │
      │ "cevap ver"│ │ zorunlu    │ │ gönderme   │
      │ talimatı   │ │ değil      │ │            │
      └────────────┘ └────────────┘ └────────────┘
```

## Dosya Yapısı

```
agent-chat/
├── server.py           # MCP sunucusu
├── orchestrator.py     # Python orchestrator (4 pane desteği)
├── start.sh            # tmux başlatıcı (4 pane)
├── requirements.txt    # Python bağımlılıkları
├── README.md           # Kullanım kılavuzu
└── docs/
    ├── ARCHITECTURE.md # Bu dosya
    └── MANAGER_PROMPT.md # Yönetici Claude prompt'u
```

## Veri Dosyaları

```
/tmp/agent-chat-room/
├── messages.json       # Tüm mesajlar
├── agents.json         # Aktif agent'lar
├── agent_panes.json    # Agent → Pane mapping
└── orchestrator_state.json  # Son işlenen mesaj ID
```

## Kurulum ve Başlatma

```bash
# 1. tmux session başlat (4 pane)
./start.sh
tmux attach -t agents

# 2. Pane 0: Orchestrator
./orchestrator.py --clear
./orchestrator.py --assign yonetici 1
./orchestrator.py --assign backend 2
./orchestrator.py --assign frontend 3
./orchestrator.py --watch

# 3. Pane 1: Yönetici Claude
claude
# Sonra: Yönetici prompt'unu ver (docs/MANAGER_PROMPT.md)

# 4. Pane 2: Backend Claude
cd /backend/proje
claude
# Sonra: "backend olarak agent chat odasına katıl"

# 5. Pane 3: Frontend Claude
cd /frontend/proje
claude
# Sonra: "frontend olarak agent chat odasına katıl"
```
