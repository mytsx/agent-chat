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
  // is the DetectCLIs result (each entry's `available` marks it installed).
  const availableCLIs = useTerminals((s) => s.availableCLIs);
  const detectionLoaded = availableCLIs.length > 0;
  const installed = new Set(
    availableCLIs.filter((c) => c.available).map((c) => c.type)
  );
  const installedTargets = ALL_TARGETS.filter(
    (c) => c !== currentCLI && installed.has(c)
  );
  // Distinguish "detection hasn't run yet" from "genuinely no other installed CLI"
  // (Codex P3 #3): while unloaded, fall back to ALL_TARGETS so the dialog isn't
  // empty during load; once loaded with zero installed alternatives, offer nothing
  // and disable confirm — the backend preflight would reject every uninstalled
  // target anyway.
  const noAlternatives = detectionLoaded && installedTargets.length === 0;
  const targets =
    installedTargets.length > 0
      ? installedTargets
      : detectionLoaded
      ? []
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
        {noAlternatives ? (
          <p className="form-error">Başka kurulu CLI yok — geçiş yapılamıyor.</p>
        ) : (
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
        )}
        {err && <p className="form-error">{err}</p>}
        <div className="modal-actions">
          <button className="btn" onClick={doSwitch} disabled={busy || noAlternatives}>
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
