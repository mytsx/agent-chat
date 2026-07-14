import { useEffect, useState } from "react";
import {
  GetVoiceStatus,
  SetVoiceConfig,
  GetDeferralEnabled,
  SetDeferralEnabled,
  GetUsageThresholds,
  SetUsageThresholds,
} from "../../wailsjs/go/main/App";
import { VoiceStatus } from "../lib/types";
import { errorToString } from "../lib/errorText";

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
  const [warnPct, setWarnPct] = useState(85);
  const [critPct, setCritPct] = useState(95);

  useEffect(() => {
    GetVoiceStatus()
      .then(setStatus)
      .catch((e) => setError(errorToString(e)));
    GetDeferralEnabled()
      .then(setDeferralEnabled)
      .catch(() => {});
    GetUsageThresholds()
      .then((t) => {
        setWarnPct(t.warnPercent ?? 85);
        setCritPct(t.criticalPercent ?? 95);
      })
      .catch(() => {});
  }, []);

  // Persist thresholds on blur, not on every keystroke — saving each edit causes
  // excessive disk I/O + IPC. Persisting only the settled value on blur also avoids
  // the transient empty-input divergence (Number("")===0). The backend normalizes
  // (0/out-of-range → 85/95, crit ≥ warn), so a settled invalid value is coerced.
  const saveThresholds = async (w: number, c: number) => {
    try {
      await SetUsageThresholds(w, c);
      // Re-fetch the normalized values (backend coerces 0/out-of-range → 85/95 and
      // enforces crit ≥ warn) so the inputs reflect what was actually stored instead
      // of the raw invalid entry lingering until the modal reopens (Gemini).
      const t = await GetUsageThresholds();
      setWarnPct(t?.warnPercent ?? 85);
      setCritPct(t?.criticalPercent ?? 95);
    } catch (e) {
      console.error("Eşik kaydedilemedi", e);
      setError(errorToString(e));
    }
  };

  const handleDeferralToggle = async (checked: boolean) => {
    setDeferralEnabled(checked);
    try {
      await SetDeferralEnabled(checked);
    } catch (e) {
      setError(errorToString(e));
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
      setError(errorToString(e));
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
          <label>Usage uyarı eşikleri (Codex %)</label>
          <div style={{ display: "flex", gap: 8 }}>
            <label>
              Uyarı{" "}
              <input
                type="number"
                min={1}
                max={100}
                value={warnPct}
                onChange={(e) => setWarnPct(Number(e.target.value))}
                onBlur={() => saveThresholds(warnPct, critPct)}
                style={{ width: 64 }}
              />
            </label>
            <label>
              Kritik{" "}
              <input
                type="number"
                min={1}
                max={100}
                value={critPct}
                onChange={(e) => setCritPct(Number(e.target.value))}
                onBlur={() => saveThresholds(warnPct, critPct)}
                style={{ width: 64 }}
              />
            </label>
          </div>
          <span className="form-hint">
            Codex limitin bu yüzdesine ulaşınca rozet sararır/kızarır ve geçiş
            önerilir. Token-tabanlı CLI'lar (Claude/Copilot/Gemini) yalnız gösterim
            amaçlıdır.
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
