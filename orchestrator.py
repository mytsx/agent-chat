#!/usr/bin/env python3
"""
Agent Chat Orchestrator - Yönetici Claude Destekli
Mesaj kuyruğunu izler ve Yönetici Claude'a bildirir.

4 Pane Yapısı:
- Pane 0: Bu orchestrator
- Pane 1: Yönetici Claude
- Pane 2: Backend Claude
- Pane 3: Frontend/Mobil Claude
"""

import json
import time
import subprocess
import argparse
from pathlib import Path

CHAT_DIR = Path("/tmp/agent-chat-room")
MESSAGES_FILE = CHAT_DIR / "messages.json"
AGENTS_FILE = CHAT_DIR / "agents.json"
STATE_FILE = CHAT_DIR / "orchestrator_state.json"

TMUX_SESSION = "agents"

# Yönetici agent adı
MANAGER_AGENT = "yonetici"

def is_pane_ready(pane: int, timeout: int = 60) -> bool:
    """Claude'un hazır olup olmadığını kontrol et."""
    start = time.time()
    while time.time() - start < timeout:
        result = subprocess.run(
            ["tmux", "capture-pane", "-t", f"{TMUX_SESSION}:0.{pane}", "-p", "-S", "-5"],
            capture_output=True,
            text=True
        )
        last_lines = result.stdout.strip()

        # Meşgul işaretleri
        busy_indicators = [
            "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
            "Working", "Thinking", "Running",
        ]

        is_busy = any(indicator in last_lines for indicator in busy_indicators)

        if is_busy:
            print(f"  ⏳ Pane {pane} meşgul, bekleniyor...")
            time.sleep(2)
            continue

        return True

    print(f"  ⚠️ Pane {pane} timeout - yine de gönderiliyor")
    return True

def send_to_pane(pane: int, text: str):
    """Belirli bir pane'e metin gönder."""
    is_pane_ready(pane)

    target = f"{TMUX_SESSION}:0.{pane}"

    result1 = subprocess.run(["tmux", "send-keys", "-t", target, "-l", "--", text],
                             capture_output=True)
    time.sleep(0.3)

    result2 = subprocess.run(["tmux", "send-keys", "-t", target, "C-m"],
                             capture_output=True)

    if result1.returncode != 0 or result2.returncode != 0:
        print(f"  ❌ Hata! tmux send-keys başarısız")
    else:
        print(f"  → Pane {pane}'e gönderildi: {text[:60]}...")

def get_agent_pane_mapping() -> dict:
    """Agent -> Pane mapping."""
    mapping_file = CHAT_DIR / "agent_panes.json"
    if mapping_file.exists():
        return json.loads(mapping_file.read_text())
    return {}

def set_agent_pane(agent_name: str, pane: int):
    """Agent'ı pane'e ata."""
    mapping = get_agent_pane_mapping()
    mapping[agent_name] = pane
    mapping_file = CHAT_DIR / "agent_panes.json"
    mapping_file.write_text(json.dumps(mapping, indent=2))
    print(f"✓ {agent_name} → Pane {pane}")

def get_last_processed_id() -> int:
    """Son işlenen mesaj ID."""
    if STATE_FILE.exists():
        state = json.loads(STATE_FILE.read_text())
        return state.get("last_processed_id", 0)
    return 0

def save_last_processed_id(msg_id: int):
    """Son işlenen mesaj ID kaydet."""
    STATE_FILE.write_text(json.dumps({"last_processed_id": msg_id}))

def get_messages() -> list:
    """Tüm mesajlar."""
    if not MESSAGES_FILE.exists():
        return []
    try:
        return json.loads(MESSAGES_FILE.read_text())
    except:
        return []

def get_agents() -> dict:
    """Aktif agent'lar."""
    if not AGENTS_FILE.exists():
        return {}
    try:
        return json.loads(AGENTS_FILE.read_text())
    except:
        return {}

def process_new_messages():
    """Yeni mesajları işle - Yönetici Claude'a bildir."""
    last_id = get_last_processed_id()
    messages = get_messages()
    mapping = get_agent_pane_mapping()

    new_messages = [m for m in messages if m["id"] > last_id]

    for msg in new_messages:
        msg_id = msg["id"]
        from_agent = msg["from"]
        to_agent = msg["to"]
        content = msg["content"][:100]
        msg_type = msg.get("type", "direct")

        # Sistem mesajlarını atla
        if msg_type == "system":
            save_last_processed_id(msg_id)
            continue

        print(f"\n📨 Mesaj #{msg_id}: {from_agent} → {to_agent}")
        print(f"   İçerik: {content[:60]}...")

        # Yönetici'den gelen mesajlar = Talimat
        # Bu mesajlar doğrudan hedef agent'a gider
        if from_agent == MANAGER_AGENT:
            # Yönetici talimatı - hedef agent'a bildir
            if to_agent != "all" and to_agent in mapping:
                pane = mapping[to_agent]
                # Yönetici'nin mesajını agent'a ilet
                prompt = f'Yönetici talimatı: "{content}" - Mesajları oku ve gereğini yap.'
                send_to_pane(pane, prompt)
            elif to_agent == "all":
                # Broadcast talimat - yönetici hariç herkese
                for agent, pane in mapping.items():
                    if agent != MANAGER_AGENT:
                        prompt = f'Yönetici talimatı: "{content}" - Mesajları oku.'
                        send_to_pane(pane, prompt)
                        time.sleep(0.5)
        else:
            # Normal mesaj - Yönetici'ye bildir (analiz etsin)
            if MANAGER_AGENT in mapping:
                manager_pane = mapping[MANAGER_AGENT]
                prompt = f'Yeni mesaj var ({from_agent} → {to_agent}). Mesajları kontrol et ve gerekli yönlendirmeleri yap.'
                send_to_pane(manager_pane, prompt)

        save_last_processed_id(msg_id)

def watch_loop():
    """Ana izleme döngüsü."""
    mapping = get_agent_pane_mapping()

    print("👀 Mesaj kuyruğu izleniyor... (Ctrl+C ile çık)")
    print(f"   Mesaj dosyası: {MESSAGES_FILE}")
    print(f"   Agent mapping: {mapping}")

    if MANAGER_AGENT not in mapping:
        print(f"\n⚠️  UYARI: '{MANAGER_AGENT}' pane'e atanmamış!")
        print(f"   Çalıştır: ./orchestrator.py --assign {MANAGER_AGENT} 1")

    print()

    while True:
        try:
            process_new_messages()
            time.sleep(1)
        except KeyboardInterrupt:
            print("\n👋 Orchestrator durduruluyor...")
            break
        except Exception as e:
            print(f"❌ Hata: {e}")
            time.sleep(2)

def setup_tmux_session():
    """tmux session kur (4 pane)."""
    print("🚀 tmux session (4 pane) kuruluyor...")

    subprocess.run(["tmux", "kill-session", "-t", TMUX_SESSION], capture_output=True)

    subprocess.run(["tmux", "new-session", "-d", "-s", TMUX_SESSION, "-n", "chat"])

    # 4 pane (2x2 grid)
    subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
    subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.0"])
    subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.1"])

    # Mouse desteği
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "mouse", "on"])

    print("""
✅ tmux session hazır! (4 pane)

┌──────────────┬──────────────┐
│  Pane 0      │  Pane 1      │
│ Orchestrator │  Yönetici    │
├──────────────┼──────────────┤
│  Pane 2      │  Pane 3      │
│  Backend     │  Frontend    │
└──────────────┴──────────────┘

Şimdi:
  tmux attach -t agents

Sonra:
  1. Pane 0: ./orchestrator.py --assign yonetici 1
  2. Pane 0: ./orchestrator.py --assign backend 2
  3. Pane 0: ./orchestrator.py --assign frontend 3
  4. Pane 0: ./orchestrator.py --watch

  5. Pane 1: claude → Yönetici prompt yapıştır
  6. Pane 2: claude → "backend olarak odaya katıl"
  7. Pane 3: claude → "frontend olarak odaya katıl"
""")

def clear_state():
    """State temizle."""
    CHAT_DIR.mkdir(parents=True, exist_ok=True)
    for f in [MESSAGES_FILE, AGENTS_FILE, STATE_FILE, CHAT_DIR / "agent_panes.json"]:
        if f.exists():
            f.unlink()
    print("🧹 Tüm state temizlendi.")

def main():
    parser = argparse.ArgumentParser(description="Agent Chat Orchestrator (Yönetici Claude Destekli)")
    parser.add_argument("--setup", action="store_true", help="tmux session kur (4 pane)")
    parser.add_argument("--watch", action="store_true", help="Mesaj kuyruğunu izle")
    parser.add_argument("--assign", nargs=2, metavar=("AGENT", "PANE"), help="Agent'ı pane'e ata")
    parser.add_argument("--clear", action="store_true", help="State temizle")
    parser.add_argument("--status", action="store_true", help="Durum göster")

    args = parser.parse_args()

    CHAT_DIR.mkdir(parents=True, exist_ok=True)

    if args.setup:
        setup_tmux_session()
    elif args.watch:
        watch_loop()
    elif args.assign:
        agent_name, pane = args.assign
        set_agent_pane(agent_name, int(pane))
    elif args.clear:
        clear_state()
    elif args.status:
        print("📊 Durum:")
        print(f"   Agents: {get_agents()}")
        print(f"   Pane mapping: {get_agent_pane_mapping()}")
        print(f"   Messages: {len(get_messages())}")
        print(f"   Last processed: {get_last_processed_id()}")
    else:
        parser.print_help()

if __name__ == "__main__":
    main()
