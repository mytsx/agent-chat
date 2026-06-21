import { useEffect, useRef, useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { Team } from "../lib/types";
import RoomCharterModal from "./RoomCharterModal";

export default function TabBar() {
  const {
    teams,
    activeTeamID,
    setActiveTeam,
    createTeam,
    deleteTeam,
    removeTeamLocal,
    setCustomPrompt,
    saveSession,
  } = useTeams();
  const { removeAllForTeam } = useTerminals();
  const [showCreate, setShowCreate] = useState(false);
  const [editingTeam, setEditingTeam] = useState<Team | null>(null);
  const [savingTeamID, setSavingTeamID] = useState<string | null>(null);
  const [saveMsg, setSaveMsg] = useState<string | null>(null);
  // Track the toast auto-dismiss timer so a rapid second save cancels the first
  // one's pending clear instead of letting it null the newer message early.
  const saveMsgTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(saveMsgTimer.current), []);

  // Two-step create: the team is created first, then its charter is persisted via
  // the dedicated SetCustomPrompt endpoint (kept separate so the charter has a
  // single write path shared with editing).
  const handleCreate = async (name: string, charter: string) => {
    const t = await createTeam(name, "2x2", []);
    if (charter.trim()) {
      try {
        await setCustomPrompt(t.id, charter);
      } catch (e) {
        // Best-effort rollback so a charter-persist failure doesn't leave an
        // orphan room (and a retry won't create a duplicate). Swallow any rollback
        // error so the ORIGINAL charter error is what the modal surfaces, not a
        // secondary delete failure.
        try {
          await deleteTeam(t.id);
        } catch {
          // Backend delete also failed (e.g. the same disk error) — deleteTeam
          // only filters client state after its binding resolves, so drop the
          // optimistic tab locally to avoid a stale/duplicate room.
          removeTeamLocal(t.id);
        }
        throw e;
      }
    }
  };

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    // Snapshot the session BEFORE tearing terminals down: removeAllForTeam closes
    // every terminal, which makes the agents leave the hub room and empties the
    // roster the snapshot (#28) is meant to capture. Best-effort — a snapshot
    // failure must not block the delete.
    try {
      await saveSession(id);
    } catch (err) {
      if (import.meta.env.DEV) console.warn("pre-delete saveSession failed:", err);
    }
    await removeAllForTeam(id);
    await deleteTeam(id);
  };

  const handleEdit = (t: Team, e: React.MouseEvent) => {
    e.stopPropagation();
    setEditingTeam(t);
  };

  // Manually snapshot the active room's session (immutable on-disk copy for #29).
  // Reentrancy-guarded; the transient result note auto-dismisses.
  const handleSaveSession = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (savingTeamID) return;
    setSavingTeamID(id);
    setSaveMsg(null);
    try {
      const r = await saveSession(id);
      setSaveMsg(
        r.saved
          ? `💾 Session kaydedildi (${r.count} mesaj)`
          : "ℹ️ Kaydedilecek yeni içerik yok"
      );
    } catch (err) {
      setSaveMsg("⚠️ Session kaydedilemedi");
      if (import.meta.env.DEV) console.warn("saveSession failed:", err);
    } finally {
      setSavingTeamID(null);
      window.clearTimeout(saveMsgTimer.current);
      saveMsgTimer.current = window.setTimeout(() => setSaveMsg(null), 2800);
    }
  };

  return (
    <div className="tab-bar">
      {teams.map((t) => (
        <div
          key={t.id}
          className={`tab ${t.id === activeTeamID ? "tab-active" : ""}`}
          onClick={() => setActiveTeam(t.id)}
        >
          <span className="tab-name">{t.name}</span>
          {t.id === activeTeamID && (
            <button
              className="tab-save"
              title="Session'u kaydet (odanın değişmez anlık görüntüsü)"
              onClick={(e) => handleSaveSession(t.id, e)}
              disabled={savingTeamID === t.id}
            >
              💾
            </button>
          )}
          {t.id === activeTeamID && (
            <button
              className="tab-edit"
              title="Oda açıklamasını düzenle"
              onClick={(e) => handleEdit(t, e)}
            >
              ✎
            </button>
          )}
          {teams.length > 1 && (
            <button
              className="tab-close"
              onClick={(e) => handleDelete(t.id, e)}
            >
              x
            </button>
          )}
        </div>
      ))}

      <button className="tab-add" onClick={() => setShowCreate(true)}>
        +
      </button>

      {saveMsg && <span className="tab-save-msg">{saveMsg}</span>}

      {showCreate && (
        <RoomCharterModal
          mode="create"
          onClose={() => setShowCreate(false)}
          onSubmit={handleCreate}
        />
      )}

      {editingTeam && (
        <RoomCharterModal
          mode="edit"
          initialName={editingTeam.name}
          initialCharter={editingTeam.custom_prompt ?? ""}
          onClose={() => setEditingTeam(null)}
          onSubmit={(_name, charter) =>
            setCustomPrompt(editingTeam.id, charter)
          }
        />
      )}
    </div>
  );
}
