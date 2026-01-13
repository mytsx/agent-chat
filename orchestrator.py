#!/usr/bin/env python3
"""
Agent Chat Orchestrator - Dinamik Pane + Opsiyonel Yonetici

Calisma Modlari:
1. --manager: Yonetici Claude aktif - tum mesajlar yoneticiye bildirilir
2. (default): Yonetici yok - mesajlar dogrudan hedef agent'a bildirilir

Kullanim:
    ./orchestrator.py --watch              # Yonetici olmadan
    ./orchestrator.py --watch --manager    # Yonetici ile
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
CONFIG_FILE = CHAT_DIR / "setup_config.json"

TMUX_SESSION = "agents"

# Yonetici agent adi
MANAGER_AGENT = "yonetici"

# Mesaj analiz limitleri
ACK_MSG_MAX_LENGTH = 80          # Kisa onay mesaji limiti
CONTENT_PREVIEW_LIMIT = 60       # Genel onizleme limiti
MANAGER_MSG_LIMIT = 100          # Yonetici mesaj limiti
DIRECT_MSG_PREVIEW_LIMIT = 50    # Dogrudan mesaj onizleme

# Tesekkur/onay pattern'leri - sonsuz donguyu onlemek icin
ACK_PATTERNS = [
    "tesekkur", "sagol", "eyvallah", "tamam", "anladim", "ok", "oldu",
    "super", "harika", "mukemmel", "guzel", "rica ederim", "bir sey degil",
    "thanks", "thank you", "got it", "okay", "perfect", "great",
    "tamamdir", "anlasildi", "gorusuruz", "iyi calismalar",
    "evet", "hayir", "peki", "olur", "elbette"
]

# Soru pattern'leri - bunlar kesinlikle bildirilmeli
QUESTION_PATTERNS = [
    "?", "nasil", "neden", "ne zaman", "nerede", "kim", "hangi",
    "yapabilir mi", "mumkun mu", "var mi", "bilir mi", "ister mi",
    "how", "what", "when", "where", "who", "which", "can you", "could you"
]


def is_pane_ready(pane: int, timeout: int = 60) -> bool:
    """Claude'un hazir olup olmadigini kontrol et."""
    start = time.time()
    while time.time() - start < timeout:
        result = subprocess.run(
            ["tmux", "capture-pane", "-t", f"{TMUX_SESSION}:0.{pane}", "-p", "-S", "-5"],
            capture_output=True,
            text=True
        )
        last_lines = result.stdout.strip()

        # Mesgul isaretleri
        busy_indicators = [
            "\u280b", "\u2819", "\u2839", "\u2838", "\u283c", "\u2834", "\u2826", "\u2827", "\u2807", "\u280f",
            "Working", "Thinking", "Running",
        ]

        is_busy = any(indicator in last_lines for indicator in busy_indicators)

        if is_busy:
            print(f"  Pane {pane} mesgul, bekleniyor...")
            time.sleep(2)
            continue

        return True

    print(f"  Pane {pane} timeout - yine de gonderiliyor")
    return True


def send_to_pane(pane: int, text: str):
    """Belirli bir pane'e metin gonder."""
    is_pane_ready(pane)

    target = f"{TMUX_SESSION}:0.{pane}"

    result1 = subprocess.run(["tmux", "send-keys", "-t", target, "-l", "--", text],
                             capture_output=True)
    time.sleep(0.3)

    result2 = subprocess.run(["tmux", "send-keys", "-t", target, "C-m"],
                             capture_output=True)

    if result1.returncode != 0 or result2.returncode != 0:
        print(f"  Hata! tmux send-keys basarisiz")
    else:
        preview = text[:CONTENT_PREVIEW_LIMIT] + "..." if len(text) > CONTENT_PREVIEW_LIMIT else text
        print(f"  -> Pane {pane}'e gonderildi: {preview}")


def get_agent_pane_mapping() -> dict:
    """Agent -> Pane mapping."""
    mapping_file = CHAT_DIR / "agent_panes.json"
    if mapping_file.exists():
        return json.loads(mapping_file.read_text())
    return {}


def set_agent_pane(agent_name: str, pane: int):
    """Agent'i pane'e ata."""
    mapping = get_agent_pane_mapping()
    mapping[agent_name] = pane
    mapping_file = CHAT_DIR / "agent_panes.json"
    mapping_file.write_text(json.dumps(mapping, indent=2))
    print(f"  {agent_name} -> Pane {pane}")


def get_last_processed_id() -> int:
    """Son islenen mesaj ID."""
    if STATE_FILE.exists():
        state = json.loads(STATE_FILE.read_text())
        return state.get("last_processed_id", 0)
    return 0


def save_last_processed_id(msg_id: int):
    """Son islenen mesaj ID kaydet."""
    STATE_FILE.write_text(json.dumps({"last_processed_id": msg_id}))


def get_messages() -> list:
    """Tum mesajlar."""
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


def analyze_message(msg: dict) -> dict:
    """
    Mesaji analiz et ve ne yapilacagina karar ver.

    Returns:
        {"action": "skip" | "notify", "reason": str, "is_question": bool}
    """
    content = msg["content"]
    content_lower = content.lower()
    expects_reply = msg.get("expects_reply", True)

    # Soru mu?
    is_question = any(p in content_lower for p in QUESTION_PATTERNS)

    # Kisa ack mesaji mi?
    is_short = len(content) < ACK_MSG_MAX_LENGTH
    has_ack = any(p in content_lower for p in ACK_PATTERNS)
    is_ack = is_short and has_ack and not is_question

    # Karar ver
    if is_ack and not expects_reply:
        return {"action": "skip", "reason": "Onay/tesekkur (expects_reply=False)", "is_question": False}
    elif is_ack:
        return {"action": "skip", "reason": "Kisa onay mesaji", "is_question": False}
    elif is_question:
        return {"action": "notify", "reason": "Soru - cevap gerekli", "is_question": True}
    elif expects_reply:
        return {"action": "notify", "reason": "Cevap bekleniyor", "is_question": False}
    else:
        return {"action": "notify", "reason": "Bilgilendirme", "is_question": False}


def process_message_with_manager(msg: dict, mapping: dict):
    """
    Yonetici modu: Mesaji yoneticiye bildir, o karar versin.
    """
    from_agent = msg["from"]
    to_agent = msg["to"]
    content = msg["content"][:MANAGER_MSG_LIMIT]
    msg_id = msg["id"]

    # Yoneticiden gelen mesajlar = Talimat, dogrudan hedefe git
    if from_agent == MANAGER_AGENT:
        if to_agent != "all" and to_agent in mapping:
            pane = mapping[to_agent]
            prompt = f'Yonetici talimat gonderdi. Mesajlari oku ve geregi yap.'
            send_to_pane(pane, prompt)
        elif to_agent == "all":
            for agent, pane in mapping.items():
                if agent != MANAGER_AGENT:
                    prompt = f'Yonetici talimat gonderdi. Mesajlari oku.'
                    send_to_pane(pane, prompt)
                    time.sleep(0.5)
    else:
        # Normal mesaj - Yoneticiye bildir
        if MANAGER_AGENT in mapping:
            manager_pane = mapping[MANAGER_AGENT]
            prompt = f'Yeni mesaj var ({from_agent} -> {to_agent}). Mesajlari kontrol et ve gerekli yonlendirmeleri yap.'
            send_to_pane(manager_pane, prompt)


def process_message_direct(msg: dict, mapping: dict):
    """
    Dogrudan mod: Mesaji hedef agent'a bildir (yonetici yok).
    """
    from_agent = msg["from"]
    to_agent = msg["to"]
    content = msg["content"][:ACK_MSG_MAX_LENGTH]
    msg_id = msg["id"]

    preview = content[:DIRECT_MSG_PREVIEW_LIMIT] + "..." if len(content) > DIRECT_MSG_PREVIEW_LIMIT else content

    if to_agent == "all":
        # Broadcast - herkese bildir (gonderen haric)
        for agent, pane in mapping.items():
            if agent != from_agent:
                prompt = f'{from_agent} mesaj gonderdi: "{preview}" - Mesajlari oku.'
                send_to_pane(pane, prompt)
                time.sleep(0.5)
    elif to_agent in mapping:
        # Direkt mesaj - sadece hedefe bildir
        pane = mapping[to_agent]
        prompt = f'{from_agent} sana mesaj gonderdi: "{preview}" - Mesajlari oku ve cevapla.'
        send_to_pane(pane, prompt)


def process_new_messages(with_manager: bool):
    """Yeni mesajlari isle."""
    last_id = get_last_processed_id()
    messages = get_messages()
    mapping = get_agent_pane_mapping()

    new_messages = [m for m in messages if m["id"] > last_id]

    for msg in new_messages:
        msg_id = msg["id"]
        from_agent = msg["from"]
        to_agent = msg["to"]
        content = msg["content"][:MANAGER_MSG_LIMIT]
        msg_type = msg.get("type", "direct")

        # Sistem mesajlarini atla
        if msg_type == "system":
            save_last_processed_id(msg_id)
            continue

        print(f"\n Mesaj #{msg_id}: {from_agent} -> {to_agent}")
        print(f"   Icerik: {content[:CONTENT_PREVIEW_LIMIT]}...")

        # Mesaji analiz et
        analysis = analyze_message(msg)
        print(f"   Analiz: {analysis['action']} ({analysis['reason']})")

        # Skip karari verildiyse bildirim gonderme
        if analysis["action"] == "skip":
            print(f"   Atlandi - sonsuz dongu onlendi")
            save_last_processed_id(msg_id)
            continue

        # Mesaji isle
        if with_manager:
            process_message_with_manager(msg, mapping)
        else:
            process_message_direct(msg, mapping)

        save_last_processed_id(msg_id)


def watch_loop(with_manager: bool):
    """Ana izleme dongusu."""
    mapping = get_agent_pane_mapping()

    mode = "YONETICI MODU" if with_manager else "DOGRUDAN MOD"

    print(f"\n{'='*50}")
    print(f"  ORCHESTRATOR - {mode}")
    print(f"{'='*50}")
    print(f"  Mesaj dosyasi: {MESSAGES_FILE}")
    print(f"  Agent mapping: {mapping}")

    if with_manager and MANAGER_AGENT not in mapping:
        print(f"\n  UYARI: '{MANAGER_AGENT}' pane'e atanmamis!")
        print(f"  Calistir: ./orchestrator.py --assign {MANAGER_AGENT} 1")

    print(f"\n  Mesaj kuyrugu izleniyor... (Ctrl+C ile cik)\n")

    while True:
        try:
            process_new_messages(with_manager)
            time.sleep(1)
        except KeyboardInterrupt:
            print("\n  Orchestrator durduruluyor...")
            break
        except Exception as e:
            print(f"  Hata: {e}")
            time.sleep(2)


def setup_tmux_session():
    """tmux session kur (eski 4 pane modu - geriye uyumluluk)."""
    print("  tmux session (4 pane) kuruluyor...")

    subprocess.run(["tmux", "kill-session", "-t", TMUX_SESSION], capture_output=True)
    subprocess.run(["tmux", "new-session", "-d", "-s", TMUX_SESSION, "-n", "chat"])

    # 4 pane (2x2 grid)
    subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
    subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.0"])
    subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.1"])

    # Mouse destegi
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "mouse", "on"])

    print("""
  tmux session hazir! (4 pane)

  +---------------+---------------+
  |  Pane 0       |  Pane 1       |
  | Orchestrator  |  Agent/Yon.   |
  +---------------+---------------+
  |  Pane 2       |  Pane 3       |
  |  Agent        |  Agent        |
  +---------------+---------------+

  Simdi: tmux attach -t agents
""")


def clear_state():
    """State temizle."""
    CHAT_DIR.mkdir(parents=True, exist_ok=True)
    for f in [MESSAGES_FILE, AGENTS_FILE, STATE_FILE, CHAT_DIR / "agent_panes.json"]:
        if f.exists():
            f.unlink()
    print("  Tum state temizlendi.")


def main():
    parser = argparse.ArgumentParser(
        description="Agent Chat Orchestrator - Dinamik + Opsiyonel Yonetici",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Modlar:
  --watch              Mesajlari izle (dogrudan mod - yonetici yok)
  --watch --manager    Mesajlari izle (yonetici modu)

Ornekler:
  ./orchestrator.py --watch              # 2 agent birbirine mesaj atar
  ./orchestrator.py --watch --manager    # Yonetici koordine eder
  ./orchestrator.py --assign backend 2   # backend -> Pane 2
        """
    )

    parser.add_argument("--setup", action="store_true",
                        help="tmux session kur (4 pane)")
    parser.add_argument("--watch", action="store_true",
                        help="Mesaj kuyruğunu izle")
    parser.add_argument("--manager", "-m", action="store_true",
                        help="Yonetici modu (varsayilan: dogrudan mod)")
    parser.add_argument("--assign", nargs=2, metavar=("AGENT", "PANE"),
                        help="Agent'i pane'e ata")
    parser.add_argument("--clear", action="store_true",
                        help="State temizle")
    parser.add_argument("--status", action="store_true",
                        help="Durum goster")

    args = parser.parse_args()

    CHAT_DIR.mkdir(parents=True, exist_ok=True)

    if args.setup:
        setup_tmux_session()
    elif args.watch:
        watch_loop(with_manager=args.manager)
    elif args.assign:
        agent_name, pane = args.assign
        set_agent_pane(agent_name, int(pane))
    elif args.clear:
        clear_state()
    elif args.status:
        print("  Durum:")
        print(f"   Agents: {get_agents()}")
        print(f"   Pane mapping: {get_agent_pane_mapping()}")
        print(f"   Messages: {len(get_messages())}")
        print(f"   Last processed: {get_last_processed_id()}")
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
