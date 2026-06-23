import { useState } from "react";
import { RoomSummary, AgentConfig } from "../lib/types";
import { useTeams } from "../store/useTeams";

// Mirror of internal/validation.ValidateName: ASCII letters/digits/._- and space,
// 1-50 chars, no leading dot, no "..". Turkish chars are rejected — a room whose name
// fails this cannot be imported, because team name must equal room name for the new
// team to subscribe to the existing room and inherit its history.
function isValidName(name: string): boolean {
  if (!/^[a-zA-Z0-9._\- ]{1,50}$/.test(name)) return false;
  if (name.includes("..")) return false;
  if (name.startsWith(".")) return false;
  return true;
}

type Row = { id: string; name: string; role: string; cli_type: string };

// Stable, unique row ids so React keys don't shift when rows are added/removed
// (array-index keys cause input focus loss / stale values in dynamic lists).
let rowCounter = 0;
const newRow = (init?: Partial<Row>): Row => ({
  id: `row-${rowCounter++}`,
  name: "",
  role: "",
  cli_type: "claude",
  ...init,
});

function seedRows(room: RoomSummary): Row[] {
  const agentNames = Object.keys(room.agents || {});
  if (agentNames.length > 0) {
    return agentNames.map((n) => newRow({ name: n, role: room.agents[n]?.role || "" }));
  }
  if (room.historical_agents?.length > 0) {
    return room.historical_agents.map((n) => newRow({ name: n }));
  }
  return [];
}

export default function ImportRoomModal({
  room,
  onClose,
  onImported,
}: {
  room: RoomSummary;
  onClose: () => void;
  onImported: () => void;
}) {
  const createTeam = useTeams((s) => s.createTeam);
  const [rows, setRows] = useState<Row[]>(() => seedRows(room));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const nameOk = isValidName(room.name);

  const update = (i: number, patch: Partial<Row>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const remove = (i: number) => setRows((rs) => rs.filter((_, j) => j !== i));
  const add = () => setRows((rs) => [...rs, newRow()]);

  const submit = async () => {
    setBusy(true);
    setErr(null);
    try {
      const agents: AgentConfig[] = rows
        .filter((r) => r.name.trim() !== "")
        .map((r, i) => ({
          name: r.name.trim(),
          role: r.role.trim(),
          prompt_id: "",
          work_dir: "",
          cli_type: r.cli_type,
          slot_index: i,
          use_worktree: false,
        }));
      await createTeam(room.name, "2x2", agents);
      onImported();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal import-modal" onClick={(e) => e.stopPropagation()}>
        <h3>Takıma Aktar: {room.name}</h3>
        {!nameOk && (
          <p className="import-error">
            Oda adı geçersiz karakter içeriyor (yalnız ASCII harf/rakam/._- ve boşluk).
            İçe aktarılamaz.
          </p>
        )}
        <div className="import-rows">
          {rows.map((r, i) => (
            <div key={r.id} className="import-row">
              <input
                placeholder="ad"
                value={r.name}
                onChange={(e) => update(i, { name: e.target.value })}
              />
              <input
                placeholder="rol"
                value={r.role}
                onChange={(e) => update(i, { role: e.target.value })}
              />
              <select
                value={r.cli_type}
                onChange={(e) => update(i, { cli_type: e.target.value })}
              >
                <option value="claude">claude</option>
                <option value="gemini">gemini</option>
                <option value="copilot">copilot</option>
              </select>
              <button onClick={() => remove(i)} title="Kaldır">
                ✕
              </button>
            </div>
          ))}
          <button className="import-add" onClick={add}>
            + Agent ekle
          </button>
        </div>
        {err && <p className="import-error">İçe aktarma başarısız: {err}</p>}
        <div className="modal-actions">
          <button onClick={onClose} disabled={busy}>
            İptal
          </button>
          <button onClick={submit} disabled={busy || !nameOk}>
            {busy ? "Aktarılıyor…" : "Takım oluştur"}
          </button>
        </div>
      </div>
    </div>
  );
}
