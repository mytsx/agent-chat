import { useUsageFor } from "../store/useUsage";
import { UsageWindow } from "../lib/types";

function fmtTokens(n: number): string {
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}
function windowLabel(w: UsageWindow): string {
  const m = w.windowMinutes;
  if (m >= 10080) return `${Math.round(m / 10080)}h`; // hafta
  if (m >= 1440) return `${Math.round(m / 1440)}g`;
  if (m >= 60) return `${Math.round(m / 60)}s`;
  return `${m}dk`;
}
function resetLabel(resetsAt: number): string {
  if (!resetsAt) return "";
  const d = new Date(resetsAt * 1000);
  return d.toLocaleString();
}

export default function UsageBadge({ sessionID }: { sessionID: string }) {
  const ev = useUsageFor(sessionID);
  if (!ev) return null;
  const s = ev.snapshot;

  // Codex: renkli yüzde rozeti
  if (s.kind === 1) {
    const wins = [s.primary, s.secondary].filter(Boolean) as UsageWindow[];
    if (wins.length === 0) return null;
    const max = Math.max(...wins.map((w) => w.usedPercent));
    const cls = ev.status === 3 ? "critical" : ev.status === 2 ? "warn" : "ok";
    const title =
      wins
        .map(
          (w) =>
            `${windowLabel(w)} penceresi: %${w.usedPercent.toFixed(0)}${
              w.resetsAt ? ` · sıfırlanma ${resetLabel(w.resetsAt)}` : ""
            }`,
        )
        .join("\n") +
      (s.model ? `\nModel: ${s.model}` : "") +
      (s.planType ? `\nPlan: ${s.planType}` : "");
    return (
      <span className={`usage-badge usage-badge-${cls}`} title={title}>
        %{max.toFixed(0)}
      </span>
    );
  }

  // Token-CLI: renksiz tüketim rozeti
  if (s.kind === 2) {
    const total = (s.inputTokens ?? 0) + (s.outputTokens ?? 0);
    if (total === 0) return null;
    const title = `Girdi: ${s.inputTokens ?? 0} · Çıktı: ${s.outputTokens ?? 0} · Cache: ${
      s.cacheTokens ?? 0
    }${s.model ? `\nModel: ${s.model}` : ""}`;
    return (
      <span className="usage-badge usage-badge-token" title={title}>
        {fmtTokens(total)} tok
      </span>
    );
  }
  return null;
}
