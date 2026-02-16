import { useEffect, useState } from "react";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { parseGrid, gridCapacity } from "../lib/types";
import TerminalPane from "./TerminalPane";
import GridSelector from "./GridSelector";

export default function TerminalGrid() {
  const { teams, activeTeamID, updateTeam } = useTeams();
  const { sessions, addTerminal, removeTerminal } = useTerminals();
  const [addingAgent, setAddingAgent] = useState(false);
  const [agentName, setAgentName] = useState("");

  const team = teams.find((t) => t.id === activeTeamID);
  if (!team) return <div className="terminal-grid-empty">No team selected</div>;

  const teamSessions = sessions[team.id] ?? [];
  const { cols, rows } = parseGrid(team.grid_layout);
  const capacity = gridCapacity(team.grid_layout);

  const handleLayoutChange = async (layout: string) => {
    await updateTeam(team.id, team.name, layout, team.agents);
  };

  const handleAddTerminal = async () => {
    if (teamSessions.length >= capacity) return;
    const name = agentName.trim() || `agent-${teamSessions.length + 1}`;
    await addTerminal(team.id, name, "");
    setAgentName("");
    setAddingAgent(false);
  };

  const handleRemoveTerminal = async (sessionID: string) => {
    await removeTerminal(team.id, sessionID);
  };

  return (
    <div className="terminal-grid-wrapper">
      <div className="terminal-grid-toolbar">
        <GridSelector current={team.grid_layout} onChange={handleLayoutChange} />
        <div className="terminal-grid-actions">
          {addingAgent ? (
            <div className="add-agent-input">
              <input
                autoFocus
                value={agentName}
                onChange={(e) => setAgentName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleAddTerminal();
                  if (e.key === "Escape") setAddingAgent(false);
                }}
                placeholder="Agent name..."
              />
              <button onClick={handleAddTerminal}>Add</button>
            </div>
          ) : (
            <button
              className="btn-add-terminal"
              onClick={() => setAddingAgent(true)}
              disabled={teamSessions.length >= capacity}
            >
              + Terminal ({teamSessions.length}/{capacity})
            </button>
          )}
        </div>
      </div>

      <div
        className="terminal-grid"
        style={{
          display: "grid",
          gridTemplateColumns: `repeat(${cols}, 1fr)`,
          gridTemplateRows: `repeat(${rows}, 1fr)`,
          gap: "2px",
          flex: 1,
        }}
      >
        {teamSessions.map((s) => (
          <TerminalPane
            key={s.sessionID}
            sessionID={s.sessionID}
            agentName={s.agentName}
          />
        ))}
        {teamSessions.length < capacity &&
          Array.from({ length: capacity - teamSessions.length }).map((_, i) => (
            <div key={`empty-${i}`} className="terminal-empty">
              <button
                className="terminal-empty-add"
                onClick={() => setAddingAgent(true)}
              >
                + Add Terminal
              </button>
            </div>
          ))}
      </div>
    </div>
  );
}
