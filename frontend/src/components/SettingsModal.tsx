import { useEffect, useState } from "react";
import { GetVoiceStatus, SetVoiceConfig } from "../../wailsjs/go/main/App";
import { VoiceStatus } from "../lib/types";

interface SettingsModalProps {
  onClose: () => void;
}

// SettingsModal edits the OpenAI Whisper API key for voice prompts (#16). The raw
// key is never read back from the backend — only a masked hint + ffmpeg presence.
// Reuses the shared .modal / .form-group / .modal-actions styles.
export default function SettingsModal({ onClose }: SettingsModalProps) {
  const [status, setStatus] = useState<VoiceStatus | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    GetVoiceStatus().then(setStatus).catch((e) => setError(String(e)));
  }, []);

  const handleSave = async () => {
    // Mirror the Save button's disabled state here too: pressing Enter on the empty,
    // auto-focused field would otherwise call SetVoiceConfig("") and wipe a stored
    // key (Codex P2). Empty input is a no-op, not a clear.
    if (saving || apiKey.trim() === "") return;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await SetVoiceConfig(apiKey.trim());
      setApiKey("");
      setSaved(true);
      const fresh = await GetVoiceStatus();
      setStatus(fresh);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>⚙️ Ayarlar — Sesli Prompt</h3>

        <div className="form-group">
          <label>OpenAI API Anahtarı</label>
          <input
            type="password"
            autoFocus
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSave();
              if (e.key === "Escape") onClose();
            }}
            placeholder={status?.hasKey ? `Kayıtlı: ${status.keyHint}` : "sk-..."}
          />
          <span className="form-hint">
            {status?.hasKey
              ? `✅ Anahtar kayıtlı (${status.keyHint}). Değiştirmek için yenisini girin.`
              : "ℹ️ Whisper STT için OpenAI anahtarı gerekir. ~/.agent-chat/voice.json'a kaydedilir."}
          </span>
          <span className="form-hint">
            {status?.ffmpegFound
              ? "✅ ffmpeg bulundu."
              : "⚠️ ffmpeg bulunamadı — sesli prompt için: brew install ffmpeg"}
          </span>
        </div>

        {error && <div className="form-error">⚠️ {error}</div>}
        {saved && <div className="form-hint">✅ Kaydedildi.</div>}

        <div className="modal-actions">
          <button className="btn" onClick={handleSave} disabled={saving || apiKey.trim() === ""}>
            Kaydet
          </button>
          <button className="btn btn-secondary" onClick={onClose}>
            Kapat
          </button>
        </div>
      </div>
    </div>
  );
}
