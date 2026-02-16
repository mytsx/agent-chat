import { useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";

export default function TabBar() {
  const { teams, activeTeamID, setActiveTeam, createTeam, deleteTeam } =
    useTeams();
  const { removeAllForTeam } = useTerminals();
  const [showNew, setShowNew] = useState(false);
  const [newName, setNewName] = useState("");

  const handleCreate = async () => {
    if (!newName.trim()) return;
    await createTeam(newName.trim(), "2x2", []);
    setNewName("");
    setShowNew(false);
  };

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    await removeAllForTeam(id);
    await deleteTeam(id);
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

      {showNew ? (
        <div className="tab-new-input">
          <input
            autoFocus
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleCreate();
              if (e.key === "Escape") setShowNew(false);
            }}
            placeholder="Team name..."
          />
        </div>
      ) : (
        <button className="tab-add" onClick={() => setShowNew(true)}>
          +
        </button>
      )}
    </div>
  );
}
