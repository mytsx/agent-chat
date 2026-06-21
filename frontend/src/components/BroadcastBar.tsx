import { useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";

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

  const handleSend = async () => {
    if (!text.trim() || !activeTeamID || busy) return;
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

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
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
        disabled={busy}
      />
      <label
        className="broadcast-bar-toggle"
        title="Açık: her terminalde Enter'a basılır (otomatik gönderir). Kapalı (varsayılan): metin input satırında bekler, her panelde siz onaylarsınız."
      >
        <input
          type="checkbox"
          checked={submit}
          onChange={(e) => setSubmit(e.target.checked)}
        />
        Enter ile gönder
      </label>
      <button
        className="broadcast-bar-send"
        onClick={handleSend}
        disabled={busy || !text.trim() || !activeTeamID}
        title="Metni tüm agent terminallerine yaz"
      >
        {busy ? "Gönderiliyor…" : "Gönder"}
      </button>
      {error && (
        <span className="broadcast-bar-error" title={error}>
          ⚠️ {error}
        </span>
      )}
    </div>
  );
}
