import { useState } from "react";

interface RoomCharterModalProps {
  mode: "create" | "edit";
  // edit: the existing room name (shown, not editable here — name editing is out
  // of scope for the charter feature).
  initialName?: string;
  initialCharter?: string;
  onClose: () => void;
  // create: (name, charter). edit: name is ignored, only charter is persisted.
  onSubmit: (name: string, charter: string) => Promise<void>;
}

// RoomCharterModal collects a room's name (create only) and free-text charter —
// the start-of-room context/mission injected into every new agent's startup
// prompt. Reuses the shared .modal / .form-group / .modal-actions styles.
export default function RoomCharterModal({
  mode,
  initialName = "",
  initialCharter = "",
  onClose,
  onSubmit,
}: RoomCharterModalProps) {
  const [name, setName] = useState(initialName);
  const [charter, setCharter] = useState(initialCharter);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isCreate = mode === "create";
  const canSubmit = isCreate ? name.trim().length > 0 : true;
  const isDirty = name !== initialName || charter !== initialCharter;

  // Mirror of the backend maxCharterLen (internal/team/store.go). Counted in code
  // points ([...]) to match the Go rune cap — charter.length / HTML maxLength count
  // UTF-16 units, which diverge for non-BMP chars. The backend stays the source of
  // truth; this is a UX affordance so the user sees impending truncation.
  const CHARTER_MAX = 2000;
  const charterLen = [...charter].length;

  // Guard accidental dismissal (overlay click / Escape / Cancel) when the form
  // has unsaved edits, so a long charter isn't lost by a stray click. confirm()
  // mirrors the app's existing use of blocking dialogs (alert in TerminalGrid).
  const requestClose = () => {
    if (
      isDirty &&
      !window.confirm("Kaydedilmemiş değişiklikler kaybolacak. Kapatılsın mı?")
    ) {
      return;
    }
    onClose();
  };

  const handleSubmit = async () => {
    if (!canSubmit || saving) return;
    setSaving(true);
    setError(null);
    try {
      await onSubmit(name.trim(), charter);
      onClose();
    } catch (e) {
      // Surface binding/persist failures in-modal (matches BroadcastBar). On
      // create, handleCreate has already rolled back the orphan team.
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={requestClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>{isCreate ? "Yeni Oda" : `Oda Açıklaması — ${initialName}`}</h3>

        {isCreate && (
          <div className="form-group">
            <label>Oda Adı</label>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") handleSubmit();
                if (e.key === "Escape") requestClose();
              }}
              placeholder="Backend Ekibi"
            />
          </div>
        )}

        <div className="form-group">
          <label>
            Oda Açıklaması / Başlangıç Bağlamı{" "}
            <span className="form-hint">(opsiyonel)</span>
          </label>
          <textarea
            autoFocus={!isCreate}
            value={charter}
            onChange={(e) => setCharter(e.target.value)}
            onKeyDown={(e) => {
              // ⌘/Ctrl+Enter submits (plain Enter inserts newlines); matches
              // BroadcastBar's multi-line textarea convention.
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                handleSubmit();
              }
              if (e.key === "Escape") requestClose();
            }}
            rows={10}
            placeholder="Bu oda neden kuruldu, hangi proje/görev konuşuluyor, agent'lardan ne bekleniyor?"
          />
          <span className={"form-counter" + (charterLen > CHARTER_MAX ? " over" : "")}>
            {charterLen}/{CHARTER_MAX}
          </span>
          <span className="form-hint">
            ℹ️ Yeni başlatılan <strong>Claude/Gemini</strong> agent'larının
            başlangıç prompt'una eklenir; çalışan terminaller ile Copilot/shell
            agent'ları etkilenmez. ⌘/Ctrl+Enter ile kaydedebilirsiniz.
          </span>
        </div>

        {error && <div className="form-error">⚠️ {error}</div>}

        <div className="modal-actions">
          <button
            className="btn"
            onClick={handleSubmit}
            disabled={!canSubmit || saving}
          >
            {isCreate ? "Oluştur" : "Kaydet"}
          </button>
          <button className="btn btn-secondary" onClick={requestClose}>
            İptal
          </button>
        </div>
      </div>
    </div>
  );
}
