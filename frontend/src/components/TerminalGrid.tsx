import { useEffect } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle } from "react-resizable-panels";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { parseGrid, gridCapacity, TerminalSession } from "../lib/types";
import TerminalPane from "./TerminalPane";
import SetupWizard from "./SetupWizard";
import GridSelector from "./GridSelector";

type GridSlot =
  | { type: "terminal"; session: TerminalSession }
  | { type: "wizard"; slotIndex: number };

export default function TerminalGrid() {
  const { teams, activeTeamID, updateTeam } = useTeams();
  const { sessions, focusedSessionID, toggleFocusSession, setFocusedSession, loadCLIs } = useTerminals();

  const team = teams.find((t) => t.id === activeTeamID);

  // Load available CLIs on mount
  useEffect(() => {
    loadCLIs();
  }, [loadCLIs]);

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

  // Build slot array: terminals first, then wizards for empty slots
  const slots: GridSlot[] = [];
  for (let i = 0; i < capacity; i++) {
    if (i < teamSessions.length) {
      slots.push({ type: "terminal", session: teamSessions[i] });
    } else {
      slots.push({ type: "wizard", slotIndex: i });
    }
  }

  // Build row groups
  const slotRows: GridSlot[][] = [];
  for (let r = 0; r < rows; r++) {
    const row = slots.slice(r * cols, (r + 1) * cols);
    if (row.length > 0) {
      slotRows.push(row);
    }
  }

  const renderFocusedMode = () => {
    const focused = teamSessions.find((s) => s.sessionID === focusedSessionID);
    if (!focused) return null;

    return (
      <div className="terminal-grid-focused">
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
              cliType={s.cliType}
              isFocused={s.sessionID === focusedSessionID}
              onToggleFocus={() => toggleFocusSession(s.sessionID)}
            />
          </div>
        ))}
      </div>
    );
  };

  const renderSlot = (slot: GridSlot) => {
    if (slot.type === "terminal") {
      return (
        <TerminalPane
          sessionID={slot.session.sessionID}
          agentName={slot.session.agentName}
          cliType={slot.session.cliType}
          isFocused={false}
          onToggleFocus={() => toggleFocusSession(slot.session.sessionID)}
        />
      );
    }
    return (
      <SetupWizard
        slotIndex={slot.slotIndex}
        teamID={team.id}
        onCreated={() => {}}
      />
    );
  };

  const renderResizablePanels = () => {
    return (
      <PanelGroup orientation="vertical" className="terminal-panel-group">
        {slotRows.map((rowSlots, rowIdx) => (
          <PanelGroupRow key={rowIdx} rowIdx={rowIdx}>
            {rowSlots.map((slot, colIdx) => {
              const key =
                slot.type === "terminal"
                  ? slot.session.sessionID
                  : `wizard-${slot.slotIndex}`;
              return (
                <PanelItem key={key} colIdx={colIdx}>
                  {renderSlot(slot)}
                </PanelItem>
              );
            })}
          </PanelGroupRow>
        ))}
      </PanelGroup>
    );
  };

  return (
    <div className="terminal-grid-wrapper">
      <div className="terminal-grid-toolbar">
        <GridSelector current={team.grid_layout} onChange={handleLayoutChange} />
        <span className="terminal-count">
          {teamSessions.length}/{capacity}
        </span>
      </div>

      {focusedSessionID && teamSessions.some((s) => s.sessionID === focusedSessionID) ? (
        renderFocusedMode()
      ) : (
        <div className="terminal-grid-content">
          {renderResizablePanels()}
        </div>
      )}
    </div>
  );
}

// Helper: a row in the vertical PanelGroup
function PanelGroupRow({
  children,
  rowIdx,
}: {
  children: React.ReactNode;
  rowIdx: number;
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
}: {
  children: React.ReactNode;
  colIdx: number;
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
