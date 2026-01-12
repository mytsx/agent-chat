#!/usr/bin/env python3
"""
Agent Chat Setup - Dinamik Agent Baslatma

Kullanim:
    ./setup.py 3                     # 3 agent (pane 0: orchestrator, 1-3: agents)
    ./setup.py 3 --manager           # 3 agent + yonetici (pane 1: yonetici, 2-4: agents)
    ./setup.py 2 --names backend,mobile
    ./setup.py 3 --manager --names backend,mobile,web
"""

import argparse
import subprocess
import json
import sys
from pathlib import Path

CHAT_DIR = Path("/tmp/agent-chat-room")
TMUX_SESSION = "agents"
CONFIG_DIR = Path(__file__).parent / "config"
BASE_PROMPT_FILE = CONFIG_DIR / "base_prompt.txt"

DEFAULT_AGENT_NAMES = ["backend", "frontend", "mobile", "web", "devops", "qa", "design", "data"]


def clear_state():
    """Onceki state'i temizle."""
    CHAT_DIR.mkdir(parents=True, exist_ok=True)
    for f in ["messages.json", "agents.json", "orchestrator_state.json", "agent_panes.json"]:
        filepath = CHAT_DIR / f
        if filepath.exists():
            filepath.unlink()
    print("  Onceki state temizlendi")


def setup_tmux_session(num_agents: int, with_manager: bool):
    """
    tmux session olustur.

    Pane yapisi:
    - with_manager=False: Pane 0: orchestrator, Pane 1-N: agents
    - with_manager=True:  Pane 0: orchestrator, Pane 1: yonetici, Pane 2-N+1: agents
    """
    total_panes = num_agents + 1  # +1 for orchestrator
    if with_manager:
        total_panes += 1  # +1 for manager

    # Kill existing session
    subprocess.run(["tmux", "kill-session", "-t", TMUX_SESSION],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    # Create new session
    subprocess.run(["tmux", "new-session", "-d", "-s", TMUX_SESSION, "-n", "chat"])

    # Calculate grid layout
    # For 2-4 panes: 2x2 grid
    # For 5-6 panes: 2x3 grid
    # For 7-9 panes: 3x3 grid

    if total_panes <= 2:
        # Split horizontal
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
    elif total_panes <= 4:
        # 2x2 grid
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.0"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.2"])
    elif total_panes <= 6:
        # 2x3 grid
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0.1"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.0"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.2"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.4"])
    else:
        # 3x3 grid
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0"])
        subprocess.run(["tmux", "split-window", "-h", "-t", f"{TMUX_SESSION}:0.1"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.0"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.2"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.4"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.1"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.3"])
        subprocess.run(["tmux", "split-window", "-v", "-t", f"{TMUX_SESSION}:0.5"])

    # Enable mouse
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "mouse", "on"])

    # Enable pane titles (persistent)
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "pane-border-status", "top"])
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "pane-border-format", " #{pane_index}: #{pane_title} "])
    subprocess.run(["tmux", "set-option", "-t", TMUX_SESSION, "allow-rename", "off"])
    subprocess.run(["tmux", "set-window-option", "-t", TMUX_SESSION, "automatic-rename", "off"])

    return total_panes


def save_config(num_agents: int, with_manager: bool, agent_names: list):
    """Konfigurasyonu kaydet."""
    config = {
        "num_agents": num_agents,
        "with_manager": with_manager,
        "agent_names": agent_names
    }

    config_file = CHAT_DIR / "setup_config.json"
    config_file.write_text(json.dumps(config, indent=2))

    # Agent-pane mapping olustur
    mapping = {}

    if with_manager:
        mapping["yonetici"] = 1
        for i, name in enumerate(agent_names):
            mapping[name] = i + 2  # Pane 2'den basla
    else:
        for i, name in enumerate(agent_names):
            mapping[name] = i + 1  # Pane 1'den basla

    mapping_file = CHAT_DIR / "agent_panes.json"
    mapping_file.write_text(json.dumps(mapping, indent=2))

    return mapping


def get_base_prompt() -> str:
    """Base prompt'u oku."""
    if BASE_PROMPT_FILE.exists():
        return BASE_PROMPT_FILE.read_text()
    return ""


def set_pane_titles(with_manager: bool, agent_names: list):
    """Pane basliklarini ayarla."""
    # Pane 0 her zaman orchestrator
    subprocess.run(["tmux", "select-pane", "-t", f"{TMUX_SESSION}:0.0", "-T", "ORCHESTRATOR"])

    if with_manager:
        subprocess.run(["tmux", "select-pane", "-t", f"{TMUX_SESSION}:0.1", "-T", "YONETICI"])
        for i, name in enumerate(agent_names):
            pane_num = i + 2
            subprocess.run(["tmux", "select-pane", "-t", f"{TMUX_SESSION}:0.{pane_num}", "-T", name.upper()])
    else:
        for i, name in enumerate(agent_names):
            pane_num = i + 1
            subprocess.run(["tmux", "select-pane", "-t", f"{TMUX_SESSION}:0.{pane_num}", "-T", name.upper()])


def print_instructions(num_agents: int, with_manager: bool, agent_names: list, mapping: dict):
    """Kullanim talimatlari yazdir."""

    print(f"\n{'='*60}")
    print("AGENT CHAT SETUP TAMAMLANDI")
    print(f"{'='*60}\n")

    # Pane diagram
    if with_manager:
        print("Pane Yapisi:")
        print("+" + "-"*20 + "+" + "-"*20 + "+")
        print(f"| Pane 0: Orchestrator | Pane 1: Yonetici   |")
        print("+" + "-"*20 + "+" + "-"*20 + "+")

        for i in range(0, len(agent_names), 2):
            left = f"Pane {i+2}: {agent_names[i]}" if i < len(agent_names) else ""
            right = f"Pane {i+3}: {agent_names[i+1]}" if i+1 < len(agent_names) else ""
            print(f"| {left:<18} | {right:<18} |")
        print("+" + "-"*20 + "+" + "-"*20 + "+")
    else:
        print("Pane Yapisi:")
        print("+" + "-"*20 + "+" + "-"*20 + "+")
        print(f"| Pane 0: Orchestrator |" + " "*20 + "|")
        print("+" + "-"*20 + "+" + "-"*20 + "+")

        for i in range(0, len(agent_names), 2):
            left = f"Pane {i+1}: {agent_names[i]}" if i < len(agent_names) else ""
            right = f"Pane {i+2}: {agent_names[i+1]}" if i+1 < len(agent_names) else ""
            print(f"| {left:<18} | {right:<18} |")
        print("+" + "-"*20 + "+" + "-"*20 + "+")

    print(f"\nAgent-Pane Mapping: {mapping}")

    print("\n" + "-"*60)
    print("SONRAKI ADIMLAR:")
    print("-"*60)

    print("\n1. tmux session'a baglan:")
    print(f"   tmux attach -t {TMUX_SESSION}")

    print("\n2. Pane 0'da orchestrator'u baslat:")
    if with_manager:
        print("   ./orchestrator.py --watch --manager")
    else:
        print("   ./orchestrator.py --watch")

    if with_manager:
        print("\n3. Pane 1'de Yonetici Claude'u baslat:")
        print("   claude")
        print("   # Sonra yonetici prompt'unu yapistir (docs/MANAGER_PROMPT.md)")

    agent_start_pane = 2 if with_manager else 1
    print(f"\n{'4' if with_manager else '3'}. Agent'lari baslat (Pane {agent_start_pane}+):")

    for name, pane in mapping.items():
        if name == "yonetici":
            continue
        print(f"   Pane {pane}: claude")
        print(f"           # '{name}' olarak agent-chat'e katil")

    print("\n" + "-"*60)
    print("HIZLI BASLATMA (her pane'de):")
    print("-"*60)
    print("""
Her agent icin:
  1. cd /proje/dizini
  2. claude
  3. "Sen [ROL]'sun. agent-chat'e '[ISIM]' olarak katil."
""")


def main():
    parser = argparse.ArgumentParser(
        description="Agent Chat Setup - Dinamik Agent Baslatma",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Ornekler:
  ./setup.py 3                     # 3 agent
  ./setup.py 3 --manager           # 3 agent + yonetici
  ./setup.py 2 --names backend,mobile
  ./setup.py 3 --manager --names backend,mobile,web
        """
    )

    parser.add_argument("num_agents", type=int, nargs="?", default=2,
                        help="Agent sayisi (default: 2)")
    parser.add_argument("--manager", "-m", action="store_true",
                        help="Yonetici Claude ekle")
    parser.add_argument("--names", "-n", type=str,
                        help="Agent isimleri (virgul ile ayrilmis)")
    parser.add_argument("--no-clear", action="store_true",
                        help="Onceki state'i temizleme")

    args = parser.parse_args()

    # Validate
    if args.num_agents < 1 or args.num_agents > 8:
        print("Hata: Agent sayisi 1-8 arasi olmali")
        sys.exit(1)

    # Agent names
    if args.names:
        agent_names = [n.strip() for n in args.names.split(",")]
        if len(agent_names) != args.num_agents:
            print(f"Hata: {args.num_agents} agent icin {len(agent_names)} isim verildi")
            sys.exit(1)
    else:
        agent_names = DEFAULT_AGENT_NAMES[:args.num_agents]

    print(f"\nAgent Chat Setup")
    print(f"  Agent sayisi: {args.num_agents}")
    print(f"  Yonetici: {'Evet' if args.manager else 'Hayir'}")
    print(f"  Agent'lar: {', '.join(agent_names)}")

    # Clear state
    if not args.no_clear:
        clear_state()

    # Setup tmux
    print("\ntmux session kuruluyor...")
    total_panes = setup_tmux_session(args.num_agents, args.manager)
    print(f"  {total_panes} pane olusturuldu")

    # Save config
    mapping = save_config(args.num_agents, args.manager, agent_names)

    # Set pane titles
    print("  Pane basliklari ayarlaniyor...")
    set_pane_titles(args.manager, agent_names)

    # Print instructions
    print_instructions(args.num_agents, args.manager, agent_names, mapping)


if __name__ == "__main__":
    main()
