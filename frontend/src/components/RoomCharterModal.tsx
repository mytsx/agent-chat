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

  const isCreate = mode === "create";
  const canSubmit = isCreate ? name.trim().length > 0 : true;

  const handleSubmit = async () => {
    if (!canSubmit || saving) return;
    setSaving(true);
    try {
      await onSubmit(name.trim(), charter);
      onClose();
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>
          {isCreate ? "Yeni Oda" : `Oda Açıklaması — ${initialName}`}
        </h3>

        {isCreate && (
          <div className="form-group">
            <label>Oda Adı</label>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") handleSubmit();
                if (e.key === "Escape") onClose();
              }}
              placeholder="Örn. Ödeme Servisi"
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
            rows={10}
            placeholder="Bu oda neden kuruldu, hangi proje/görev konuşuluyor, agent'lardan ne bekleniyor?"
          />
          <span className="form-hint">
            ℹ️ Açıklama yalnızca <strong>yeni başlatılan</strong> agent'lara
            uygulanır; çalışan terminaller etkilenmez.
          </span>
        </div>

        <div className="modal-actions">
          <button
            className="btn"
            onClick={handleSubmit}
            disabled={!canSubmit || saving}
          >
            {isCreate ? "Oluştur" : "Kaydet"}
          </button>
          <button className="btn btn-secondary" onClick={onClose}>
            İptal
          </button>
        </div>
      </div>
    </div>
  );
}
