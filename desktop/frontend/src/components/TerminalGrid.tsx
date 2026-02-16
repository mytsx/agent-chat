import { useEffect, useState } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle } from "react-resizable-panels";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { parseGrid, gridCapacity } from "../lib/types";
import TerminalPane from "./TerminalPane";
import GridSelector from "./GridSelector";

export default function TerminalGrid() {
  const { teams, activeTeamID, updateTeam } = useTeams();
  const { sessions, addTerminal, removeTerminal, focusedSessionID, toggleFocusSession, setFocusedSession } = useTerminals();
  const [addingAgent, setAddingAgent] = useState(false);
  const [agentName, setAgentName] = useState("");

  const team = teams.find((t) => t.id === activeTeamID);

  // ESC to unfocus
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape" && focusedSessionID) {
        setFocusedSession(null);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [focusedSessionID, setFocusedSession]);

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

  // Build rows of sessions for the grid
  const sessionRows: (typeof teamSessions)[] = [];
  for (let r = 0; r < rows; r++) {
    const rowSessions = teamSessions.slice(r * cols, (r + 1) * cols);
    if (rowSessions.length > 0) {
      sessionRows.push(rowSessions);
    }
  }

  // Count empty slots
  const emptyCount = capacity - teamSessions.length;

  const renderFocusedMode = () => {
    const focused = teamSessions.find((s) => s.sessionID === focusedSessionID);
    if (!focused) return null;

    return (
      <div className="terminal-grid-focused">
        {/* Render all panes but hide non-focused ones to keep them mounted */}
        {teamSessions.map((s) => (
          <div
            key={s.sessionID}
            style={{
              display: s.sessionID === focusedSessionID ? "flex" : "none",
              flex: 1,
            }}
          >
            <TerminalPane
              sessionID={s.sessionID}
              agentName={s.agentName}
              isFocused={s.sessionID === focusedSessionID}
              onToggleFocus={() => toggleFocusSession(s.sessionID)}
            />
          </div>
        ))}
      </div>
    );
  };

  const renderResizablePanels = () => {
    return (
      <PanelGroup orientation="vertical" className="terminal-panel-group">
        {sessionRows.map((rowSessions, rowIdx) => (
          <PanelGroupRow key={rowIdx} rowIdx={rowIdx} totalRows={sessionRows.length}>
            {rowSessions.map((s, colIdx) => (
              <PanelItem key={s.sessionID} colIdx={colIdx} totalCols={rowSessions.length}>
                <TerminalPane
                  sessionID={s.sessionID}
                  agentName={s.agentName}
                  isFocused={false}
                  onToggleFocus={() => toggleFocusSession(s.sessionID)}
                />
              </PanelItem>
            ))}
          </PanelGroupRow>
        ))}
      </PanelGroup>
    );
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

      {focusedSessionID && teamSessions.some((s) => s.sessionID === focusedSessionID) ? (
        renderFocusedMode()
      ) : (
        <div className="terminal-grid-content">
          {teamSessions.length > 0 && renderResizablePanels()}
          {emptyCount > 0 && !focusedSessionID && (
            <div className="terminal-empty-slots">
              {Array.from({ length: emptyCount }).map((_, i) => (
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
          )}
        </div>
      )}
    </div>
  );
}

// Helper: a row in the vertical PanelGroup
function PanelGroupRow({
  children,
  rowIdx,
  totalRows,
}: {
  children: React.ReactNode;
  rowIdx: number;
  totalRows: number;
}) {
  return (
    <>
      {rowIdx > 0 && (
        <PanelResizeHandle className="resize-handle resize-handle-horizontal" />
      )}
      <Panel minSize="10%">
        <PanelGroup orientation="horizontal" className="terminal-panel-row">
          {children}
        </PanelGroup>
      </Panel>
    </>
  );
}

// Helper: a column item in the horizontal PanelGroup
function PanelItem({
  children,
  colIdx,
  totalCols,
}: {
  children: React.ReactNode;
  colIdx: number;
  totalCols: number;
}) {
  return (
    <>
      {colIdx > 0 && (
        <PanelResizeHandle className="resize-handle resize-handle-vertical" />
      )}
      <Panel minSize="10%">{children}</Panel>
    </>
  );
}
