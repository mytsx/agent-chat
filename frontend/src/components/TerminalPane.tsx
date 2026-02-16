import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import { WriteToTerminal, ResizeTerminal } from "../../wailsjs/go/main/App";

interface Props {
  sessionID: string;
  agentName: string;
  isFocused?: boolean;
  onToggleFocus?: () => void;
}

export default function TerminalPane({ sessionID, agentName, isFocused, onToggleFocus }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);

  useEffect(() => {
    if (!containerRef.current || !sessionID) return;

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", Menlo, monospace',
      theme: {
        background: "#0d1117",
        foreground: "#c9d1d9",
        cursor: "#58a6ff",
        selectionBackground: "#264f78",
        black: "#0d1117",
        red: "#ff7b72",
        green: "#7ee787",
        yellow: "#d29922",
        blue: "#58a6ff",
        magenta: "#bc8cff",
        cyan: "#39c5cf",
        white: "#c9d1d9",
        brightBlack: "#484f58",
        brightRed: "#ffa198",
        brightGreen: "#56d364",
        brightYellow: "#e3b341",
        brightBlue: "#79c0ff",
        brightMagenta: "#d2a8ff",
        brightCyan: "#56d4dd",
        brightWhite: "#f0f6fc",
      },
      allowProposedApi: true,
    });

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();

    term.loadAddon(fitAddon);
    term.loadAddon(webLinksAddon);
    term.open(containerRef.current);

    termRef.current = term;
    fitRef.current = fitAddon;

    // Fit terminal to container
    setTimeout(() => {
      try {
        fitAddon.fit();
        ResizeTerminal(sessionID, term.cols, term.rows).catch(() => {});
      } catch {}
    }, 100);

    // Handle user input -> send to PTY
    term.onData((data: string) => {
      WriteToTerminal(sessionID, data).catch(() => {});
    });

    // Handle PTY output -> write to terminal
    const eventName = "pty:output:" + sessionID;
    let eventCleanup = () => {};

    import("../../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      EventsOn(eventName, (data: string) => {
        term.write(data);
      });
      eventCleanup = () => {
        try { EventsOff(eventName); } catch {}
      };
    }).catch(() => {});

    // Handle resize
    const resizeObserver = new ResizeObserver(() => {
      if (fitRef.current) {
        try {
          fitRef.current.fit();
        } catch {}
        if (termRef.current) {
          ResizeTerminal(
            sessionID,
            termRef.current.cols,
            termRef.current.rows
          ).catch(() => {});
        }
      }
    });
    resizeObserver.observe(containerRef.current);

    return () => {
      eventCleanup();
      resizeObserver.disconnect();
      term.dispose();
    };
  }, [sessionID]);

  return (
    <div className="terminal-pane">
      <div className="terminal-header">
        <span className="terminal-agent-name">{agentName || "Terminal"}</span>
        <div className="terminal-header-actions">
          {onToggleFocus && (
            <button
              className="terminal-btn-focus"
              onClick={onToggleFocus}
              title={isFocused ? "Restore" : "Maximize"}
            >
              {isFocused ? "\u229F" : "\u229E"}
            </button>
          )}
        </div>
      </div>
      <div className="terminal-container" ref={containerRef} />
    </div>
  );
}
