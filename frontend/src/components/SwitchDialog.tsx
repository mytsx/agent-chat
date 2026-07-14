import { useState } from "react";
import { CLIType } from "../lib/types";
import { errorToString } from "../lib/errorText";
import { useTerminals } from "../store/useTerminals";

const ALL_TARGETS: CLIType[] = ["codex", "claude", "copilot", "gemini"];

export default function SwitchDialog({
  currentCLI,
  onSwitch,
  onClose,
}: {
  currentCLI: CLIType;
  onSwitch: (targetCLI: CLIType) => Promise<string>;
  onClose: () => void;
}) {
  // Only offer INSTALLED CLIs: switching to an uninstalled one closes the old PTY
  // and then createTerminal fails, leaving the user with no terminal. availableCLIs
  // is the DetectCLIs result (each entry's `available` marks it installed). If the
  // list isn't loaded yet, fall back to ALL_TARGETS so the dialog never shows zero
  // options (Codex P2 #1).
  const availableCLIs = useTerminals((s) => s.availableCLIs);
  const installed = new Set(
    availableCLIs.filter((c) => c.available).map((c) => c.type)
  );
  const installedTargets = ALL_TARGETS.filter(
    (c) => c !== currentCLI && installed.has(c)
  );
  const targets =
    installedTargets.length > 0
      ? installedTargets
      : ALL_TARGETS.filter((c) => c !== currentCLI);
  const [target, setTarget] = useState<CLIType>(targets[0]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const doSwitch = async () => {
    setBusy(true);
    setErr("");
    try {
      await onSwitch(target);
      onClose();
    } catch (e) {
      setErr(errorToString(e));
      setBusy(false);
    }
  };
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        onClick={(e) => e.stopPropagation()}
      >
        <h3>⚠️ CLI Geçişi</h3>
        <p className="form-hint">
          '{currentCLI}' limitine yaklaşıyor. Aynı slotta yeni CLI ile devam et —
          oda geçmişi ve devralma notu yeni agent'a aktarılır (cross-CLI resume
          mümkün değil, yeni oturum başlar).
        </p>
        <div className="form-group">
          <label htmlFor="switch-target">Hedef CLI</label>
          <select
            id="switch-target"
            value={target}
            onChange={(e) => setTarget(e.target.value as CLIType)}
          >
            {targets.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </div>
        {err && <p className="form-error">{err}</p>}
        <div className="modal-actions">
          <button className="btn" onClick={doSwitch} disabled={busy}>
            {busy ? "Geçiliyor…" : "Geçişi onayla"}
          </button>
          <button className="btn btn-secondary" onClick={onClose} disabled={busy}>
            Yoksay
          </button>
        </div>
      </div>
    </div>
  );
}
