import { useEffect, useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { SessionInfo, CLIType } from "../lib/types";
import { overlaps, bestMatch } from "../lib/sessionCorrelation";

interface Props { teamID: string; onClose: () => void; }
type Mode = "fresh" | "last" | "custom";

// Per-agent row state: the agent config + its fetched history + the chosen session
// (undefined = fresh "✨ Yeni").
interface Row {
  agentName: string;
  cliType: CLIType;
  workDir: string;
  promptID: string;
  useWorktree: boolean;
  slotIndex: number;
  sessions: SessionInfo[];
  selected?: SessionInfo;
  open: boolean;
}

function fmt(unix: number): string {
  const d = new Date(unix * 1000);
  return d.toLocaleString("tr-TR", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });
}
function dur(sec: number): string {
  const m = Math.round(sec / 60);
  return m >= 60 ? `${Math.floor(m / 60)}s ${m % 60}dk` : `${m}dk`;
}

export default function OpenTeamModal({ teamID, onClose }: Props) {
  const team = useTeams((s) => s.teams.find((t) => t.id === teamID));
  const { listAgentSessions, openTeamFromConfigResume } = useTerminals();
  const [rows, setRows] = useState<Row[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [mode, setMode] = useState<Mode>("custom");
  const [opening, setOpening] = useState(false);
  // driver = the session the user LAST explicitly picked; it drives the 🟢 same-period
  // highlights and "set all". Derived-from-first-selected would mis-correlate after
  // "Son oturumlardan" selects every row (Codex P2). Cleared by the bulk modes.
  const [driver, setDriver] = useState<SessionInfo | undefined>();

  // Load each configured agent's session history once.
  useEffect(() => {
    if (!team) return;
    let alive = true;
    (async () => {
      // Fetch every agent's history in PARALLEL — sequential awaits made the modal
      // open slowly for multi-agent teams (each call stats+parses transcripts) (Gemini).
      const out = await Promise.all((team.agents ?? []).map(async (ag, idx) => {
        const cli = (ag.cli_type || "shell") as CLIType;
        // Only the agent's CURRENT-CLI sessions are resumable as configured — a Claude
        // session can't be resumed under a now-Codex agent (`codex resume <claude-id>`
        // would fail/open the wrong conversation). Filter so the resume always uses the
        // matching CLI (Codex P2).
        const sessions = (await listAgentSessions(teamID, ag.name)).filter((s) => s.cliType === cli);
        return {
          agentName: ag.name,
          cliType: cli,
          workDir: ag.work_dir || "",
          promptID: ag.prompt_id || "",
          useWorktree: !!ag.use_worktree,
          slotIndex: ag.slot_index ?? idx,
          sessions, open: false,
        } as Row;
      }));
      if (alive) {
        setRows(out);
        setLoaded(true);
      }
    })();
    return () => { alive = false; };
  }, [teamID]); // eslint-disable-line

  const applyMode = (m: Mode) => {
    setMode(m);
    setDriver(undefined); // bulk modes have no single explicit driver
    setRows((rs) => rs.map((r) => ({
      ...r,
      open: false, // close any open dropdown so it can't show stale pre-mode state (Copilot)
      selected: m === "fresh" ? undefined : m === "last" ? r.sessions[0] : r.selected,
    })));
  };

  const pick = (i: number, s?: SessionInfo) => {
    setMode("custom");
    setDriver(s); // the last explicit pick drives correlation (undefined for "Yeni")
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, selected: s, open: false } : r)));
  };
  const toggleOpen = (i: number) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, open: !r.open } : { ...r, open: false })));

  // Make a clickable option keyboard-activatable (Enter/Space) for a11y (Copilot).
  const onKeyActivate = (fn: () => void) => (e: { key: string; preventDefault: () => void }) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      fn();
    }
  };

  // Set every OTHER agent to its best same-period session relative to `driver`.
  const setAllSamePeriod = () => {
    if (!driver) return;
    setRows((rs) => rs.map((r) => {
      // Compare by sessionID, not object identity — state updates clone rows (Gemini).
      if (r.selected?.sessionID === driver.sessionID) return r;
      const m = bestMatch(driver, r.sessions);
      return m ? { ...r, selected: m } : r;
    }));
  };

  const open = async () => {
    if (opening) return;
    setOpening(true);
    try {
      // One backend batch call: reuses OpenTeamFromConfig's ordering/capacity/phantom
      // guards (the raw per-row loop bypassed them) and skips per-agent failures, so a
      // partial failure leaves no duplicates on retry (Codex P2). resumeIDs carries
      // only agents with a pick; the rest open fresh.
      const resumeIDs: Record<string, string> = {};
      for (const r of rows) {
        if (r.selected) resumeIDs[r.agentName] = r.selected.sessionID;
      }
      const results = await openTeamFromConfigResume(teamID, resumeIDs);
      const failed = results.filter((r) => r.error);
      // Close regardless: the successful terminals are already in the store, so
      // reopening the modal and pressing Aç again must not re-create them.
      onClose();
      if (failed.length > 0) {
        alert(`Bazı agent'lar açılamadı:\n\n${failed.map((r) => `${r.agentName}: ${r.error}`).join("\n")}`);
      }
    } catch (e) {
      console.error("[OpenTeamModal] open failed:", e);
      alert(`Açılırken hata: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setOpening(false);
    }
  };

  if (!team) return null;
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal open-team-modal" onClick={(e) => e.stopPropagation()}>
        <h3>Takımı Aç — {team.name}</h3>

        <div className="open-team-modes">
          {([["fresh", "✨ Hepsi taze"], ["last", "⏯ Son oturumlardan"], ["custom", "🎛 Özel seçim"]] as [Mode, string][]).map(([m, label]) => (
            <button key={m} className={`mode-seg ${mode === m ? "active" : ""}`} onClick={() => applyMode(m)}>{label}</button>
          ))}
        </div>

        <div className="open-team-rows">
          {rows.map((r, i) => (
            <div key={r.agentName} className="open-team-row">
              <div className="ot-agent">{r.agentName}<span className="ot-cli">{r.cliType}</span></div>
              <div className="ot-picker">
                <button className="ot-current" onClick={() => toggleOpen(i)}>
                  <span>{r.selected ? `${fmt(r.selected.startUnix)} · ${dur(r.selected.durationSec)} · ${r.selected.messageCount} mesaj` : "✨ Yeni (taze)"}</span>
                  <span>{r.open ? "▴" : "▾"}</span>
                </button>
                {r.open && (
                  <div className="ot-dropdown">
                    <div className="ot-opt" role="button" tabIndex={0} onClick={() => pick(i, undefined)} onKeyDown={onKeyActivate(() => pick(i, undefined))}>✨ Yeni (taze başlat)</div>
                    {r.sessions.length === 0 && <div className="ot-empty">Geçmiş oturum yok</div>}
                    {r.sessions.map((s) => {
                      // Correlation highlights only OTHER agents' overlapping sessions —
                      // never the driver agent's own row (#40 Faz-2). Compare by sessionID
                      // (object refs are unstable across state clones — Gemini).
                      const same = driver && r.selected?.sessionID !== driver.sessionID && overlaps(driver, s);
                      return (
                        <div key={s.sessionID} role="button" tabIndex={0} className={`ot-opt ${r.selected?.sessionID === s.sessionID ? "sel" : ""} ${same ? "same-period" : ""}`} onClick={() => pick(i, s)} onKeyDown={onKeyActivate(() => pick(i, s))}>
                          <div>{fmt(s.startUnix)} · {dur(s.durationSec)} · {s.messageCount} mesaj{same ? " 🟢" : ""}{s.fileMissing ? " ⚠️" : ""}</div>
                          {s.snippet && <div className="ot-snippet">{s.snippet}</div>}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>

        {driver && <button className="ot-setall" onClick={setAllSamePeriod}>🔗 Diğerlerini aynı döneme set et</button>}

        <div className="modal-actions">
          <button className="btn-secondary" onClick={onClose}>İptal</button>
          <button className="btn" onClick={open} disabled={opening || !loaded}>{opening ? "Açılıyor..." : !loaded ? "Yükleniyor…" : `Aç (${rows.length})`}</button>
        </div>
      </div>
    </div>
  );
}
