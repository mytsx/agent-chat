import { useEffect, useState } from "react";
import {
  GetVoiceStatus,
  SetVoiceConfig,
  GetDeferralEnabled,
  SetDeferralEnabled,
} from "../../wailsjs/go/main/App";
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
  const [deferralEnabled, setDeferralEnabled] = useState(false);

  useEffect(() => {
    GetVoiceStatus()
      .then(setStatus)
      .catch((e) => setError(String(e)));
    GetDeferralEnabled()
      .then(setDeferralEnabled)
      .catch(() => {});
  }, []);

  const handleDeferralToggle = async (checked: boolean) => {
    setDeferralEnabled(checked);
    try {
      await SetDeferralEnabled(checked);
    } catch (e) {
      setError(String(e));
      setDeferralEnabled(!checked); // geri al
    }
  };

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
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 id="settings-title">⚙️ Ayarlar</h3>

        <div className="form-group">
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={deferralEnabled}
              onChange={(e) => handleDeferralToggle(e.target.checked)}
              style={{ width: "auto", accentColor: "var(--accent)" }}
            />
            Yazarken bildirimleri ertele
          </label>
          <span className="form-hint">
            {deferralEnabled
              ? "✅ Aktif — terminalde yazı yazarken gelen bildirimler 12 saniyeye kadar beklenir; süre dolarsa ekranda gösterilir."
              : "ℹ️ Pasif (varsayılan) — bildirimler hemen terminale iletilir."}
          </span>
        </div>

        <div
          className="form-group"
          style={{
            borderTop: "1px solid var(--border)",
            paddingTop: 12,
            marginTop: 4,
          }}
        >
          <label htmlFor="voice-api-key">OpenAI API Anahtarı (Sesli Prompt)</label>
          <input
            id="voice-api-key"
            type="password"
            autoFocus
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSave();
              if (e.key === "Escape") onClose();
            }}
            placeholder={
              status?.hasKey ? `Kayıtlı: ${status.keyHint}` : "sk-..."
            }
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

        {error && <div className="form-error" role="alert">⚠️ {error}</div>}
        {saved && <div className="form-hint" role="status">✅ Kaydedildi.</div>}

        <div className="modal-actions">
          <button
            className="btn"
            type="button"
            onClick={handleSave}
            disabled={saving || apiKey.trim() === ""}
          >
            Kaydet
          </button>
          <button className="btn btn-secondary" type="button" onClick={onClose}>
            Kapat
          </button>
        </div>
      </div>
    </div>
  );
}
