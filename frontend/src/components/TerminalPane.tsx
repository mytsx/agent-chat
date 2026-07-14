import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import { WriteToTerminal, ResizeTerminal, StartVoiceCapture, StopVoiceCapture } from "../../wailsjs/go/main/App";
import { CLIType, VoiceState } from "../lib/types";
import { errorToString } from "../lib/errorText";
import UsageBadge from "./UsageBadge";

// fmtRecTime formats elapsed recording seconds as m:ss for the live timer pill.
function fmtRecTime(s: number): string {
  return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
}

interface Props {
  sessionID: string;
  agentName: string;
  cliType?: CLIType;
  isFocused?: boolean;
  onToggleFocus?: () => void;
  onRemove?: () => void;
  onRestart?: () => void;
  onResume?: () => void;
  canResume?: boolean;
}

export default function TerminalPane({ sessionID, agentName, cliType, isFocused, onToggleFocus, onRemove, onRestart, onResume, canResume }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const [voiceState, setVoiceState] = useState<VoiceState>("idle");
  const [voiceError, setVoiceError] = useState<string>("");
  // recordingRef tracks "are we currently recording" synchronously. The push-to-
  // talk handlers must NOT guard on voiceState — that's a stale closure captured at
  // render time, so a mouseup arriving before the "recording" event re-renders
  // would read "idle" and skip StopVoiceCapture, leaving ffmpeg recording forever.
  const recordingRef = useRef(false);
  // busyRef stays true from the press until the backend resolves the capture
  // (idle/error). startVoice gates on it so a second recording can't start while the
  // previous one is still transcribing — two overlapping Whisper results would both
  // inject into the same PTY line and corrupt the prompt (Codex P2).
  const busyRef = useRef(false);
  // startedRef: the backend StartVoiceCapture has resolved (activeRecorder installed).
  // releasePendingRef: the user released before the start resolved, so the stop must
  // be issued from the start's .then — otherwise StopVoiceCapture no-ops against a
  // recorder that isn't installed yet and ffmpeg is left running (Codex P2).
  const startedRef = useRef(false);
  const releasePendingRef = useRef(false);
  const [recSecs, setRecSecs] = useState(0);
  const terminalLabel = agentName || "Terminal";

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

  // Voice (#16) state/transcript events for this panel only (sessionID-scoped).
  // The transcript text is already injected into the PTY by the backend; these
  // events only drive the mic button UI, so we never write transcript to xterm.
  useEffect(() => {
    if (!sessionID) return;
    let cancelled = false;
    let cleanup = () => {};
    import("../../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      if (cancelled) return;
      const stateEv = "voice:state:" + sessionID;
      EventsOn(stateEv, (data: { state: VoiceState; message: string }) => {
        setVoiceState(data.state);
        setVoiceError(data.state === "error" ? data.message : "");
        if (data.state === "idle" || data.state === "error") {
          recordingRef.current = false;
          busyRef.current = false; // capture resolved → allow the next recording
        }
      });
      cleanup = () => {
        try { EventsOff(stateEv); } catch (e) {
          if (import.meta.env.DEV) console.warn("voice EventsOff failed:", e);
        }
      };
    }).catch((e) => {
      if (import.meta.env.DEV) console.warn("voice runtime load failed:", e);
    });
    return () => { cancelled = true; cleanup(); };
  }, [sessionID]);

  // Tick the recording timer once per second while the red pill is showing.
  useEffect(() => {
    if (voiceState !== "recording") return;
    setRecSecs(0);
    const id = setInterval(() => setRecSecs((s) => s + 1), 1000);
    return () => clearInterval(id);
  }, [voiceState]);

  // Push-to-talk: hold to record, release to transcribe. onMouseLeave also stops
  // so a drag-off doesn't leave the mic recording. Backend enforces the single
  // active-recording lock; a rejected Start just shows an error state.
  // finishStop performs the actual stop+transcribe round-trip. Cleared busyRef on the
  // backend's idle/error event, OR here (then/catch) so a missed event can't wedge the
  // pane in "transcribing" forever (Codex P2). On success it refocuses the terminal so
  // the dictated line can be edited/submitted immediately (Codex P2).
  const finishStop = () => {
    setVoiceState("transcribing");
    StopVoiceCapture(sessionID)
      .then(() => {
        busyRef.current = false;
        setVoiceState((s) => (s === "transcribing" ? "idle" : s));
        termRef.current?.focus();
      })
      .catch((e) => {
        busyRef.current = false;
        setVoiceState("error");
        setVoiceError(errorToString(e));
      });
  };
  const startVoice = () => {
    if (busyRef.current) return; // block while recording OR still transcribing
    busyRef.current = true;
    recordingRef.current = true;
    startedRef.current = false;
    releasePendingRef.current = false;
    setVoiceState("recording"); // optimistic — turn red instantly, don't wait for the event
    setVoiceError("");
    StartVoiceCapture(sessionID)
      .then(() => {
        startedRef.current = true;
        // If the user already released during this in-flight start, the deferred
        // stop runs now that the backend is actually recording (Codex P2).
        if (releasePendingRef.current) {
          releasePendingRef.current = false;
          finishStop();
        }
      })
      .catch((e) => {
        recordingRef.current = false;
        busyRef.current = false;
        releasePendingRef.current = false;
        setVoiceState("error");
        setVoiceError(errorToString(e));
      });
  };
  const stopVoice = () => {
    if (!recordingRef.current) return; // ref, not voiceState — avoids the stale-closure skip
    recordingRef.current = false;
    if (!startedRef.current) {
      // Start hasn't resolved yet — defer the stop so it isn't a no-op against a
      // recorder the backend hasn't installed yet (Codex P2).
      releasePendingRef.current = true;
      return;
    }
    finishStop();
  };

  const handleVoiceKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== " " && e.key !== "Enter") return;
    e.preventDefault();
    if (e.repeat) return;
    startVoice();
  };

  const handleVoiceKeyUp = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key !== " " && e.key !== "Enter") return;
    e.preventDefault();
    stopVoice();
  };

  const voiceButtonLabel =
    voiceState === "recording"
      ? "Recording voice prompt — release to insert"
      : voiceState === "transcribing"
      ? "Transcribing voice prompt"
      : voiceError
      ? `Voice prompt error: ${voiceError}`
      : "Voice prompt — hold to speak";

  return (
    <div className="terminal-pane">
      <div className="terminal-header">
        <span className="terminal-agent-name">{terminalLabel}</span>
        {cliType && cliType !== "shell" && (
          <span className={`cli-badge cli-badge-${cliType}`}>{cliType}</span>
        )}
        <UsageBadge sessionID={sessionID} />
        <div
          className="terminal-header-actions"
          onMouseDown={(e) => e.stopPropagation()}
        >
          <button
            type="button"
            className={"terminal-btn-voice voice-" + voiceState}
            onMouseDown={(e) => {
              e.preventDefault(); // don't let the button steal focus from xterm (Codex P2)
              startVoice();
            }}
            onMouseUp={stopVoice}
            onMouseLeave={stopVoice}
            onKeyDown={handleVoiceKeyDown}
            onKeyUp={handleVoiceKeyUp}
            onBlur={stopVoice}
            aria-label={voiceButtonLabel}
            aria-pressed={voiceState === "recording"}
            title={
              voiceState === "recording"
                ? "Recording… release to insert"
                : voiceState === "transcribing"
                ? "Transcribing…"
                : voiceError
                ? voiceError
                : "Push to talk (voice prompt)"
            }
          >
            {voiceState === "recording" ? (
              <span className="voice-pill">
                <span className="voice-wave" aria-hidden="true">
                  <i /><i /><i /><i />
                </span>
                <span className="voice-timer">{fmtRecTime(recSecs)}</span>
              </span>
            ) : voiceState === "transcribing" ? (
              <span className="voice-pill voice-pill-busy">
                <span className="voice-spinner" aria-hidden="true" />
              </span>
            ) : (
              <svg
                className="voice-mic-icon"
                width="15"
                height="15"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                <rect x="9" y="2" width="6" height="12" rx="3" />
                <path d="M5 11a7 7 0 0 0 14 0" />
                <line x1="12" y1="18" x2="12" y2="22" />
              </svg>
            )}
          </button>
          {onResume && (
            <button
              type="button"
              className="terminal-btn-resume"
              onClick={onResume}
              disabled={!canResume}
              aria-label={canResume ? `Resume ${terminalLabel} session` : `No resumable session for ${terminalLabel}`}
              title={canResume ? "Resume session (--resume)" : "No resumable session captured yet"}
            >
              {"\u23EF"}
            </button>
          )}
          {onRestart && (
            <button
              type="button"
              className="terminal-btn-restart"
              onClick={onRestart}
              aria-label={`Restart ${terminalLabel} terminal`}
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
              aria-label={isFocused ? `Restore ${terminalLabel} terminal to grid` : `Maximize ${terminalLabel} terminal`}
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
              aria-label={`Close ${terminalLabel} terminal`}
              title="Close terminal"
            >
              {"\u00D7"}
            </button>
          )}
        </div>
      </div>
      <div
        className="terminal-container"
        ref={containerRef}
        role="region"
        aria-label={`${terminalLabel} terminal session`}
      />
    </div>
  );
}
