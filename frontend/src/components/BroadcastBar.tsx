import { type KeyboardEvent, useRef, useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import {
  StartBroadcastVoiceCapture,
  StopBroadcastVoiceCapture,
} from "../../wailsjs/go/main/App";
import { VoiceState } from "../lib/types";

// BroadcastBar lets the user type once and fan the text out to every agent
// terminal of the active team at the same time — as if they typed it into each
// one. It is NOT a chat message: the backend writes straight to each PTY. With
// the "Enter ile gönder" toggle off (the default) the text is left pending in
// each terminal's input line for the user to confirm; on, each terminal also
// presses Enter automatically.
export default function BroadcastBar() {
  const activeTeamID = useTeams((s) => s.activeTeamID);
  const broadcastToTeam = useTerminals((s) => s.broadcastToTeam);
  const [text, setText] = useState("");
  const [submit, setSubmit] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [voiceState, setVoiceState] = useState<VoiceState>("idle");
  const [voiceError, setVoiceError] = useState("");

  const recordingRef = useRef(false);
  const busyVoiceRef = useRef(false);
  const startedRef = useRef(false);
  const releasePendingRef = useRef(false);

  const appendTranscript = (chunk: string) => {
    const trimmed = chunk.trim();
    if (!trimmed) return;
    setText((prev) => {
      if (!prev.trim()) return trimmed;
      return prev.endsWith("\n") ? prev + trimmed : prev + "\n" + trimmed;
    });
    if (error) setError(null);
  };

  const finishVoiceStop = () => {
    setVoiceState("transcribing");
    StopBroadcastVoiceCapture()
      .then((transcript) => {
        busyVoiceRef.current = false;
        setVoiceState((s) => (s === "transcribing" ? "idle" : s));
        appendTranscript(transcript || "");
      })
      .catch((e) => {
        busyVoiceRef.current = false;
        setVoiceState("error");
        setVoiceError(String(e));
      });
  };

  const startVoice = () => {
    if (!activeTeamID || busyVoiceRef.current) return;
    busyVoiceRef.current = true;
    recordingRef.current = true;
    startedRef.current = false;
    releasePendingRef.current = false;
    setVoiceState("recording");
    setVoiceError("");
    StartBroadcastVoiceCapture()
      .then(() => {
        startedRef.current = true;
        if (releasePendingRef.current) {
          releasePendingRef.current = false;
          finishVoiceStop();
        }
      })
      .catch((e) => {
        recordingRef.current = false;
        busyVoiceRef.current = false;
        releasePendingRef.current = false;
        setVoiceState("error");
        setVoiceError(String(e));
      });
  };

  const stopVoice = () => {
    if (!recordingRef.current) return;
    recordingRef.current = false;
    if (!startedRef.current) {
      releasePendingRef.current = true;
      return;
    }
    finishVoiceStop();
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
      ? "Toplu mesaj sesi kaydediliyor — bırakınca metne eklenir"
      : voiceState === "transcribing"
        ? "Toplu mesaj sesi çevriliyor"
        : voiceError
          ? `Toplu mesaj ses hatası: ${voiceError}`
          : "Toplu mesaj için sesli prompt — basılı tutarak konuş";
  const voiceCapturePending =
    voiceState === "recording" || voiceState === "transcribing";

  const handleSend = async () => {
    // Don't race a pending transcript: sending while Whisper is still resolving
    // can clear the textarea, then append the transcript as a stranded follow-up
    // prompt instead of including it in the intended broadcast.
    if (!text.trim() || !activeTeamID || busy || voiceCapturePending) return;
    setBusy(true);
    setError(null);
    try {
      await broadcastToTeam(activeTeamID, text, submit);
      setText("");
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // ⌘/Ctrl+Enter dispatches the broadcast; a plain Enter inserts a newline so
    // the broadcast text can be multi-line.
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      handleSend();
    }
  };

  return (
    <div className="broadcast-bar">
      <span
        className="broadcast-bar-label"
        title="Metin odadaki tüm agent terminallerine aynı anda yazılır — sohbet mesajı değil, her terminale elle yazmış gibi."
      >
        📢 Toplu mesaj
      </span>
      <textarea
        className="broadcast-bar-input"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          if (error) setError(null);
        }}
        onKeyDown={handleKeyDown}
        placeholder="Tüm agent terminallerine yazılacak metin…  (⌘/Ctrl+Enter ile gönder)"
        rows={1}
        maxLength={1000}
        disabled={busy || !activeTeamID || voiceState === "recording"}
      />
      <button
        type="button"
        className={"terminal-btn-voice broadcast-bar-voice voice-" + voiceState}
        onMouseDown={(e) => {
          e.preventDefault();
          startVoice();
        }}
        onMouseUp={stopVoice}
        onMouseLeave={stopVoice}
        disabled={busy || !activeTeamID || voiceState === "transcribing"}
        onKeyDown={handleVoiceKeyDown}
        onKeyUp={handleVoiceKeyUp}
        onBlur={stopVoice}
        aria-label={voiceButtonLabel}
        aria-pressed={voiceState === "recording"}
        title={
          voiceState === "recording"
            ? "Kaydediliyor… bırakınca metne eklenir"
            : voiceState === "transcribing"
              ? "Çevriliyor…"
              : voiceError
                ? voiceError
                : "Bas-konuş (toplu mesaj için)"
        }
      >
        {voiceState === "recording" ? (
          <span className="voice-pill">
            <span className="voice-wave" aria-hidden="true">
              <i />
              <i />
              <i />
              <i />
            </span>
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
      <label
        className="broadcast-bar-toggle"
        title="Açık: her terminalde Enter'a basılır (otomatik gönderir). Kapalı (varsayılan): metin input satırında bekler, her panelde siz onaylarsınız."
      >
        <input
          type="checkbox"
          checked={submit}
          onChange={(e) => setSubmit(e.target.checked)}
          disabled={busy || !activeTeamID}
        />
        Enter ile gönder
      </label>
      <button
        className="broadcast-bar-send"
        onClick={handleSend}
        disabled={busy || voiceCapturePending || !text.trim() || !activeTeamID}
        title={
          voiceCapturePending
            ? "Sesli prompt tamamlanınca gönderilebilir"
            : "Metni tüm agent terminallerine yaz"
        }
      >
        {busy ? "Gönderiliyor…" : "Gönder"}
      </button>
      {error && (
        <span className="broadcast-bar-error" title={error}>
          ⚠️ {error}
        </span>
      )}
      {!error && voiceError && (
        <span className="broadcast-bar-error" title={voiceError}>
          🎙 {voiceError}
        </span>
      )}
    </div>
  );
}
