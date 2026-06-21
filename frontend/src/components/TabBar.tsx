import { useState } from "react";
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
    setCustomPrompt,
  } = useTeams();
  const { removeAllForTeam } = useTerminals();
  const [showCreate, setShowCreate] = useState(false);
  const [editingTeam, setEditingTeam] = useState<Team | null>(null);

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
          // ignore — the original error below is the meaningful one
        }
        throw e;
      }
    }
  };

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    await removeAllForTeam(id);
    await deleteTeam(id);
  };

  const handleEdit = (t: Team, e: React.MouseEvent) => {
    e.stopPropagation();
    setEditingTeam(t);
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
