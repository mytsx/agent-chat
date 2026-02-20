import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import { WriteToTerminal, ResizeTerminal } from "../../wailsjs/go/main/App";
import { CLIType } from "../lib/types";

interface Props {
  sessionID: string;
  agentName: string;
  cliType?: CLIType;
  isFocused?: boolean;
  onToggleFocus?: () => void;
  onRemove?: () => void;
  onRestart?: () => void;
}

export default function TerminalPane({ sessionID, agentName, cliType, isFocused, onToggleFocus, onRemove, onRestart }: Props) {
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
        ResizeTerminal(sessionID, term.cols, term.rows).catch((e) => {
          if (import.meta.env.DEV) console.warn("Initial ResizeTerminal failed:", e);
        });
      } catch (e) {
        if (import.meta.env.DEV) console.warn("Terminal fit failed:", e);
      }
    }, 100);

    // Handle user input -> send to PTY
    term.onData((data: string) => {
      WriteToTerminal(sessionID, data).catch((e) => {
        if (import.meta.env.DEV) console.warn("WriteToTerminal failed:", e);
      });
    });

    // Handle PTY output -> write to terminal
    const eventName = "pty:output:" + sessionID;
    let cancelled = false;
    let eventCleanup = () => {};

    import("../../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      if (cancelled) return;
      EventsOn(eventName, (data: string) => {
        term.write(data);
      });
      eventCleanup = () => {
        try { EventsOff(eventName); } catch (e) {
          if (import.meta.env.DEV) console.warn("EventsOff cleanup failed:", e);
        }
      };
    }).catch((e) => {
      if (import.meta.env.DEV) console.warn("Failed to load Wails runtime:", e);
    });

    // Handle resize with debounce
    let resizeTimer: ReturnType<typeof setTimeout>;
    const resizeObserver = new ResizeObserver(() => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        if (fitRef.current) {
          try {
            fitRef.current.fit();
          } catch (e) {
            if (import.meta.env.DEV) console.warn("Terminal fit failed:", e);
          }
          if (termRef.current) {
            ResizeTerminal(
              sessionID,
              termRef.current.cols,
              termRef.current.rows
            ).catch((e) => {
              if (import.meta.env.DEV) console.warn("ResizeTerminal failed:", e);
            });
          }
        }
      }, 50);
    });
    resizeObserver.observe(containerRef.current);

    return () => {
      cancelled = true;
      eventCleanup();
      clearTimeout(resizeTimer);
      resizeObserver.disconnect();
      term.dispose();
    };
  }, [sessionID]);

  return (
    <div className="terminal-pane">
      <div className="terminal-header">
        <span className="terminal-agent-name">{agentName || "Terminal"}</span>
        {cliType && cliType !== "shell" && (
          <span className={`cli-badge cli-badge-${cliType}`}>{cliType}</span>
        )}
        <div
          className="terminal-header-actions"
          onMouseDown={(e) => e.stopPropagation()}
        >
          {onRestart && (
            <button
              type="button"
              className="terminal-btn-restart"
              onClick={onRestart}
              title="Restart terminal"
            >
              {"\u21BB"}
            </button>
          )}
          {onToggleFocus && (
            <button
              type="button"
              className="terminal-btn-focus"
              onClick={onToggleFocus}
              title={isFocused ? "Restore" : "Maximize"}
            >
              {isFocused ? "\u25A3" : "\u25A1"}
            </button>
          )}
          {onRemove && (
            <button
              type="button"
              className="terminal-btn-remove"
              onClick={onRemove}
              title="Close terminal"
            >
              {"\u00D7"}
            </button>
          )}
        </div>
      </div>
      <div className="terminal-container" ref={containerRef} />
    </div>
  );
}
