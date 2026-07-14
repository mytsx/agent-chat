import { useAllUsage } from "../store/useUsage";
import { UsageSnapshot, UsageWindow } from "../lib/types";

const STATUS_LABEL = ["—", "🟢 OK", "🟡 Uyarı", "🔴 Kritik"];

function resetCell(s: UsageSnapshot): string {
  // Show the reset of the window driving the row's status/percent — the MAX-used
  // window (pctLabel/status use max(primary, secondary)), not primary-first, so a
  // higher-used secondary window shows its own reset time (Codex P3 #5).
  const wins = [s.primary, s.secondary].filter(Boolean) as UsageWindow[];
  const w = wins.reduce<UsageWindow | undefined>(
    (best, cur) => (best && best.usedPercent >= cur.usedPercent ? best : cur),
    undefined
  );
  if (!w || !w.resetsAt) return "—";
  return new Date(w.resetsAt * 1000).toLocaleString();
}

// pctLabel: Codex → renkli-olmayan yüzde metni (max pencere), token-CLI → toplam token.
function pctLabel(s: UsageSnapshot): string {
  if (s.kind === 1) {
    const wins = [s.primary, s.secondary].filter(Boolean) as UsageWindow[];
    if (wins.length === 0) return "—";
    return `%${Math.max(...wins.map((w) => w.usedPercent)).toFixed(0)}`;
  }
  return (s.inputTokens ?? 0) + (s.outputTokens ?? 0) + " tok";
}

export default function UsagePanelModal({ onClose }: { onClose: () => void }) {
  const entries = useAllUsage();
  const rows = Object.values(entries);
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="usage-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 id="usage-title">📊 Kullanım Paneli</h3>
        {rows.length === 0 ? (
          <p className="form-hint">
            Henüz usage sinyali yok. Agent'lar çalıştıkça burada görünecek.
          </p>
        ) : (
          <table className="usage-table">
            <thead>
              <tr>
                <th>CLI</th>
                <th>Durum / %</th>
                <th>Reset</th>
                <th>Model</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((ev) => {
                const s = ev.snapshot;
                return (
                  <tr key={s.sessionID}>
                    <td>{s.cli}</td>
                    <td>
                      {s.kind === 1
                        ? `${STATUS_LABEL[ev.status]} ${pctLabel(s)}`
                        : pctLabel(s)}
                    </td>
                    <td>{resetCell(s)}</td>
                    <td>{s.model || "—"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose}>
            Kapat
          </button>
        </div>
      </div>
    </div>
  );
}
