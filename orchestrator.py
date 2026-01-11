#!/usr/bin/env python3
"""
Agent Chat Orchestrator
Mesaj kuyruğunu izler ve ilgili terminal'e komut gönderir.

Kullanım:
1. tmux session başlat: tmux new-session -s agents
2. 3 pane oluştur (Ctrl+B %)
3. Pane 0: orchestrator.py çalıştır
4. Pane 1: claude code başlat (backend)
5. Pane 2: claude code başlat (frontend)

Veya: ./orchestrator.py --setup ile otomatik kurulum
"""

import json
import time
import subprocess
import argparse
from pathlib import Path
from datetime import datetime

CHAT_DIR = Path("/tmp/agent-chat-room")
MESSAGES_FILE = CHAT_DIR / "messages.json"
AGENTS_FILE = CHAT_DIR / "agents.json"
STATE_FILE = CHAT_DIR / "orchestrator_state.json"

# tmux session ve pane mapping
TMUX_SESSION = "agents"

def run_tmux(cmd: str) -> str:
    """tmux komutu çalıştır."""
    result = subprocess.run(
        ["tmux"] + cmd.split(),
        capture_output=True,
        text=True
    )
    return result.stdout.strip()

def is_pane_ready(pane: int, timeout: int = 60) -> bool:
    """Claude'un hazır olup olmadığını kontrol et (prompt bekliyor mu?)."""
    start = time.time()
    while time.time() - start < timeout:
        # Son birkaç satırı al
        result = subprocess.run(
            ["tmux", "capture-pane", "-t", f"{TMUX_SESSION}:{0}.{pane}", "-p", "-S", "-5"],
            capture_output=True,
            text=True
        )
        last_lines = result.stdout.strip()

        # Meşgul işaretleri: spinner veya işlem yapıyor
        busy_indicators = [
            "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",  # Spinner
            "Working", "Thinking", "Running",  # İşlem yapıyor
        ]

        # Meşgul mu kontrol et
        is_busy = any(indicator in last_lines for indicator in busy_indicators)

        if is_busy:
            print(f"  ⏳ Pane {pane} meşgul, bekleniyor...")
            time.sleep(2)
            continue

        # Meşgul değilse hazır demektir
        return True

    print(f"  ⚠️ Pane {pane} timeout - yine de gönderiliyor")
    return True  # Timeout sonrası yine de gönder

def send_to_pane(pane: int, text: str):
    """Belirli bir pane'e metin gönder (Claude hazır olduğunda)."""
    # Önce Claude'un hazır olmasını bekle
    is_pane_ready(pane)

    target = f"{TMUX_SESSION}:0.{pane}"

    # Metni yaz (-l = literal, özel karakterleri yorumlama)
    result1 = subprocess.run(["tmux", "send-keys", "-t", target, "-l", "--", text],
                             capture_output=True)
    time.sleep(0.3)

    # Enter tuşuna bas (C-m = Ctrl+M = Enter)
    result2 = subprocess.run(["tmux", "send-keys", "-t", target, "C-m"],
                             capture_output=True)

    if result1.returncode != 0 or result2.returncode != 0:
        print(f"  ❌ Hata! tmux send-keys başarısız")
    else:
        print(f"  → Pane {pane}'e gönderildi: {text[:50]}...")

def get_agent_pane_mapping() -> dict:
    """Agent -> Pane mapping dosyasını oku."""
    mapping_file = CHAT_DIR / "agent_panes.json"
    if mapping_file.exists():
        return json.loads(mapping_file.read_text())
    return {}

def set_agent_pane(agent_name: str, pane: int):
    """Agent'ı bir pane'e ata."""
    mapping = get_agent_pane_mapping()
    mapping[agent_name] = pane
    mapping_file = CHAT_DIR / "agent_panes.json"
    mapping_file.write_text(json.dumps(mapping, indent=2))
    print(f"✓ {agent_name} → Pane {pane}")

def get_last_processed_id() -> int:
    """Son işlenen mesaj ID'sini al."""
    if STATE_FILE.exists():
        state = json.loads(STATE_FILE.read_text())
        return state.get("last_processed_id", 0)
    return 0

def save_last_processed_id(msg_id: int):
    """Son işlenen mesaj ID'sini kaydet."""
    STATE_FILE.write_text(json.dumps({"last_processed_id": msg_id}))

def get_messages() -> list:
    """Tüm mesajları al."""
    if not MESSAGES_FILE.exists():
        return []
    try:
        return json.loads(MESSAGES_FILE.read_text())
    except:
        return []

def get_agents() -> dict:
    """Aktif agent'ları al."""
    if not AGENTS_FILE.exists():
        return {}
    try:
        return json.loads(AGENTS_FILE.read_text())
    except:
        return {}

def process_new_messages():
    """Yeni mesajları işle ve ilgili agent'lara bildir."""
    last_id = get_last_processed_id()
    messages = get_messages()
    mapping = get_agent_pane_mapping()

    new_messages = [m for m in messages if m["id"] > last_id]

    for msg in new_messages:
        if msg["type"] == "system":
            # Sistem mesajlarını atla
            save_last_processed_id(msg["id"])
            continue

        from_agent = msg["from"]
        to_agent = msg["to"]
        content = msg["content"][:100]

        print(f"\n📨 Yeni mesaj #{msg['id']}: {from_agent} → {to_agent}")
        print(f"   İçerik: {content}")

        # Hedef agent'ı belirle
        if to_agent == "all":
            # Broadcast - gönderen hariç herkese bildir
            targets = [a for a in mapping.keys() if a != from_agent]
        else:
            targets = [to_agent] if to_agent in mapping else []

        for target in targets:
            pane = mapping.get(target)
            if pane is not None:
                # Claude Code'a mesaj oku komutu gönder
                prompt = f'{target} olarak mesajları oku ve "{from_agent}" mesajına uygun şekilde cevap ver'
                send_to_pane(pane, prompt)
                time.sleep(0.5)  # Rate limiting

        save_last_processed_id(msg["id"])

def setup_tmux_session():
    """tmux session'ı otomatik kur."""
    print("🚀 tmux session kuruluyor...")

    # Mevcut session'ı kapat
    subprocess.run(["tmux", "kill-session", "-t", TMUX_SESSION],
                   capture_output=True)

    # Yeni session oluştur (3 pane)
    subprocess.run([
        "tmux", "new-session", "-d", "-s", TMUX_SESSION, "-n", "chat"
    ])

    # Dikey split - 2 pane daha
    subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
    subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])

    # Layout düzenle
    subprocess.run(["tmux", "select-layout", "-t", f"{TMUX_SESSION}:0", "even-horizontal"])

    print("""
✅ tmux session hazır!

Şimdi:
1. Yeni terminal aç ve şunu çalıştır:
   tmux attach -t agents

2. 3 pane göreceksin:
   - Pane 0 (sol): Orchestrator için
   - Pane 1 (orta): Backend agent için
   - Pane 2 (sağ): Frontend agent için

3. Pane 1'de: cd /proje/backend && claude
4. Pane 2'de: cd /proje/frontend && claude

5. Her Claude'da odaya katıl:
   - Pane 1: "backend olarak agent chat odasına katıl"
   - Pane 2: "frontend olarak agent chat odasına katıl"

6. Agent'ları pane'lere ata (bu terminalde):
   ./orchestrator.py --assign backend 1
   ./orchestrator.py --assign frontend 2

7. Orchestrator'ı başlat:
   ./orchestrator.py --watch
""")

def watch_loop():
    """Ana izleme döngüsü."""
    print("👀 Mesaj kuyruğu izleniyor... (Ctrl+C ile çık)")
    print(f"   Mesaj dosyası: {MESSAGES_FILE}")
    print(f"   Agent mapping: {get_agent_pane_mapping()}")
    print()

    while True:
        try:
            process_new_messages()
            time.sleep(1)  # 1 saniyede bir kontrol
        except KeyboardInterrupt:
            print("\n👋 Orchestrator durduruluyor...")
            break
        except Exception as e:
            print(f"❌ Hata: {e}")
            time.sleep(2)

def clear_state():
    """State'i temizle."""
    CHAT_DIR.mkdir(parents=True, exist_ok=True)
    for f in [MESSAGES_FILE, AGENTS_FILE, STATE_FILE, CHAT_DIR / "agent_panes.json"]:
        if f.exists():
            f.unlink()
    print("🧹 Tüm state temizlendi.")

def main():
    parser = argparse.ArgumentParser(description="Agent Chat Orchestrator")
    parser.add_argument("--setup", action="store_true", help="tmux session kur")
    parser.add_argument("--watch", action="store_true", help="Mesaj kuyruğunu izle")
    parser.add_argument("--assign", nargs=2, metavar=("AGENT", "PANE"),
                        help="Agent'ı pane'e ata")
    parser.add_argument("--clear", action="store_true", help="State'i temizle")
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
