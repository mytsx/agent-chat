import { useEffect, useRef, useState } from "react";
import { useSummaries } from "../store/useSummaries";
import {
  RenderSummaryPrompt,
  GetRoomTranscript,
} from "../../wailsjs/go/main/App";

interface RoomSummaryModalProps {
  room: string;
  onClose: () => void;
}

// RoomSummaryModal is the manual session-summary workflow (#29). The summary is
// produced by a NEUTRAL observer, not a room worker: the user copies the rendered
// summary prompt (transcript + instruction), pastes it into a FRESH agent/CLI,
// then pastes the result back here and saves. "Continue" later injects the newest
// saved summary into new agents. Reuses the shared .modal / .form-group styles.
export default function RoomSummaryModal({ room, onClose }: RoomSummaryModalProps) {
  const loadSummary = useSummaries((s) => s.loadSummary);
  const saveSummary = useSummaries((s) => s.saveSummary);
  const titleId = "room-summary-title";
  const summaryEditorId = "room-summary-editor";
  const transcriptId = "room-summary-transcript";

  const [text, setText] = useState("");
  const [initialText, setInitialText] = useState("");
  const [generatedAt, setGeneratedAt] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [showTranscript, setShowTranscript] = useState(false);
  const [transcript, setTranscript] = useState<string | null>(null);
  const [busyTranscript, setBusyTranscript] = useState(false);
  const [busyCopy, setBusyCopy] = useState(false);

  // The async handlers below capture `room` at call time; if the prop changes
  // (e.g. the active team switches while the modal is open) a late completion must
  // not write the old room's data into the now-current room. roomRef always holds
  // the latest room so handlers can drop stale results.
  const roomRef = useRef(room);
  useEffect(() => {
    roomRef.current = room;
  }, [room]);

  // Tracks whether the modal is still mounted, so a late async completion (after
  // the user closed the modal) doesn't write to the clipboard or touch state.
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  // Always-current editor text, so a slow save can tell whether the user kept
  // typing after clicking Save (and avoid clobbering those edits).
  const textRef = useRef(text);
  useEffect(() => {
    textRef.current = text;
  }, [text]);

  // Counted in code points to match the Go side; the backend stays source of truth.
  const SUMMARY_MAX = 8000;
  const len = [...text].length;
  const isDirty = text !== initialText;
  const canSave = isDirty && text.trim().length > 0 && len <= SUMMARY_MAX && !saving;

  useEffect(() => {
    let alive = true;
    // Reset every field up front so switching to a different room never shows the
    // previous room's summary/transcript/notice (and can't save stale text into
    // the new room) — including when the new room has no summary or the load fails.
    setLoading(true);
    setText("");
    setInitialText("");
    setGeneratedAt(null);
    setNotice(null);
    setError(null);
    setTranscript(null);
    setShowTranscript(false);
    // Also clear in-flight busy flags so a switch mid-operation can't leave the
    // new room's modal with disabled controls.
    setBusyCopy(false);
    setBusyTranscript(false);
    setSaving(false);
    (async () => {
      const info = await loadSummary(room);
      if (!alive) return;
      const next = info?.exists ? info.text : "";
      setText(next);
      setInitialText(next);
      setGeneratedAt(info?.exists ? info.created_at : null);
      setLoading(false);
    })();
    return () => {
      alive = false;
    };
  }, [room, loadSummary]);

  const requestClose = () => {
    if (
      isDirty &&
      !window.confirm("Kaydedilmemiş değişiklikler kaybolacak. Kapatılsın mı?")
    ) {
      return;
    }
    onClose();
  };

  const handleCopyPrompt = async () => {
    if (busyCopy) return;
    const r = room;
    setBusyCopy(true);
    setError(null);
    setNotice(null);
    try {
      const prompt = await RenderSummaryPrompt(r);
      // Drop the result if the room switched OR the modal was closed mid-flight —
      // otherwise we'd overwrite the clipboard the user copied after dismissing.
      if (!aliveRef.current || roomRef.current !== r) return;
      await navigator.clipboard.writeText(prompt);
      setNotice(
        "📋 Özet promptu panoya kopyalandı. Yeni/ayrı bir agent'a (oda dışı, tarafsız) yapıştır, çıkan özeti buraya geri yapıştır."
      );
    } catch (e) {
      if (roomRef.current === r) setError(String(e));
    } finally {
      setBusyCopy(false); // always clear — the operation finished regardless of room
    }
  };

  const handleToggleTranscript = async () => {
    if (showTranscript) {
      setShowTranscript(false);
      return;
    }
    setShowTranscript(true);
    if (transcript === null && !busyTranscript) {
      const r = room;
      setBusyTranscript(true);
      try {
        const tx = await GetRoomTranscript(r);
        if (!aliveRef.current || roomRef.current !== r) return; // closed/switched: drop
        setTranscript(tx);
      } catch (e) {
        if (roomRef.current === r) {
          setError(String(e));
          setTranscript("");
        }
      } finally {
        setBusyTranscript(false); // always clear — operation finished
      }
    }
  };

  const handleSave = async () => {
    if (!canSave) return;
    const r = room;
    setSaving(true);
    setError(null);
    try {
      const submitted = text;
      const info = await saveSummary(r, submitted);
      if (!aliveRef.current || roomRef.current !== r) return; // closed/switched: don't apply
      // The persisted value (backend may sanitize / cap / normalize CRLF) becomes
      // the new dirty baseline. Only overwrite the editor with it if the user has
      // NOT typed since clicking Save — otherwise keep their in-flight edits (which
      // stay correctly marked dirty against the new baseline).
      setInitialText(info.text);
      setGeneratedAt(info.created_at);
      if (textRef.current === submitted) {
        setText(info.text);
      }
      setNotice("💾 Özet kaydedildi.");
    } catch (e) {
      if (roomRef.current === r) setError(String(e));
    } finally {
      setSaving(false); // always clear — operation finished
    }
  };

  return (
    <div className="modal-overlay" onClick={requestClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
      >
        <h3 id={titleId}>📝 Session Özeti — {room}</h3>

        <p className="form-hint">
          Özeti <strong>tarafsız bir göz</strong> üretmeli. "Özet promptunu
          kopyala" ile transcript + talimatı al, <strong>yeni/ayrı</strong> bir
          agent'a yaptır, çıkan özeti aşağıya yapıştır ve kaydet. Kaydedilen son
          özet, odaya devam edilirken yeni agent'lara otomatik verilir.
        </p>

        <div className="modal-actions" style={{ marginTop: 0 }}>
          <button className="btn btn-secondary" onClick={handleCopyPrompt} disabled={busyCopy}>
            {busyCopy ? "Hazırlanıyor…" : "📋 Özet promptunu kopyala"}
          </button>
          <button className="btn btn-secondary" onClick={handleToggleTranscript} disabled={busyTranscript}>
            {showTranscript ? "🙈 Transcript'i gizle" : "👁 Transcript'i göster"}
          </button>
        </div>

        {showTranscript && (
          <div className="form-group">
            <label htmlFor={transcriptId}>Transcript (snapshot ∪ arşiv)</label>
            <textarea
              id={transcriptId}
              readOnly
              value={busyTranscript ? "Yükleniyor…" : transcript ?? ""}
              rows={8}
            />
          </div>
        )}

        <div className="form-group">
          <label htmlFor={summaryEditorId}>
            Özet{" "}
            {generatedAt && (
              <span className="form-hint">(son kayıt: {generatedAt})</span>
            )}
          </label>
          <textarea
            id={summaryEditorId}
            autoFocus
            disabled={loading}
            value={loading ? "" : text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                handleSave();
              }
              if (e.key === "Escape") requestClose();
            }}
            rows={12}
            placeholder={
              loading
                ? "Yükleniyor…"
                : "Önceki session'ın özetini buraya yapıştır veya yaz…"
            }
          />
          <span className={"form-counter" + (len > SUMMARY_MAX ? " over" : "")}>
            {len}/{SUMMARY_MAX}
          </span>
        </div>

        {notice && <div className="form-hint" role="status">{notice}</div>}
        {error && <div className="form-error" role="alert">⚠️ {error}</div>}

        <div className="modal-actions">
          <button className="btn" onClick={handleSave} disabled={!canSave}>
            {saving ? "Kaydediliyor…" : "Kaydet"}
          </button>
          <button className="btn btn-secondary" onClick={requestClose}>
            Kapat
          </button>
        </div>
      </div>
    </div>
  );
}
