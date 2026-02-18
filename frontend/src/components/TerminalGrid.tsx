import React, { useEffect } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle } from "react-resizable-panels";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { parseGrid, gridCapacity, isCustomLayout, TerminalSession } from "../lib/types";
import TerminalPane from "./TerminalPane";
import SetupWizard from "./SetupWizard";
import GridSelector from "./GridSelector";

type GridSlot =
  | { type: "terminal"; session: TerminalSession }
  | { type: "wizard"; slotIndex: number };

export default function TerminalGrid() {
  const { teams, activeTeamID, updateTeam } = useTeams();
  const { sessions, focusedSessionID, toggleFocusSession, setFocusedSession, loadCLIs, removeTerminal, restartTerminal } = useTerminals();

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

  // Build slot array for fixed grids (skip for custom mode — capacity is Infinity)
  const slots: GridSlot[] = [];
  const slotRows: GridSlot[][] = [];
  if (!isCustomLayout(team.grid_layout)) {
    const slotMap = new Map<number, TerminalSession>();
    for (const s of teamSessions) {
      slotMap.set(s.slotIndex, s);
    }
    for (let i = 0; i < capacity; i++) {
      const session = slotMap.get(i);
      if (session) {
        slots.push({ type: "terminal", session });
      } else {
        slots.push({ type: "wizard", slotIndex: i });
      }
    }
    for (let r = 0; r < rows; r++) {
      const row = slots.slice(r * cols, (r + 1) * cols);
      if (row.length > 0) {
        slotRows.push(row);
      }
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
              onRestart={() => restartTerminal(s.teamID, s.sessionID).catch(err => console.error("[restart] failed:", err))}
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
          onRestart={() => restartTerminal(slot.session.teamID, slot.session.sessionID).catch(err => console.error("[restart] failed:", err))}
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

  const renderCustomMode = () => {
    const nextSlotIndex = teamSessions.length > 0
      ? Math.max(...teamSessions.map((s) => s.slotIndex)) + 1
      : 0;

    return (
      <PanelGroup orientation="vertical" className="terminal-panel-group">
        {teamSessions.map((s, idx) => (
          <React.Fragment key={s.sessionID}>
            {idx > 0 && (
              <PanelResizeHandle className="resize-handle resize-handle-horizontal" />
            )}
            <Panel minSize="10%">
              <TerminalPane
                sessionID={s.sessionID}
                agentName={s.agentName}
                cliType={s.cliType}
                isFocused={false}
                onToggleFocus={() => toggleFocusSession(s.sessionID)}
                onRemove={() => removeTerminal(team.id, s.sessionID)}
                onRestart={() => restartTerminal(team.id, s.sessionID).catch(err => console.error("[restart] failed:", err))}
              />
            </Panel>
          </React.Fragment>
        ))}
        {teamSessions.length > 0 && (
          <PanelResizeHandle className="resize-handle resize-handle-horizontal" />
        )}
        <Panel minSize="10%" defaultSize={teamSessions.length === 0 ? "100%" : "30%"}>
          <SetupWizard
            slotIndex={nextSlotIndex}
            teamID={team.id}
            onCreated={() => {}}
          />
        </Panel>
      </PanelGroup>
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
          {isCustomLayout(team.grid_layout)
            ? `${teamSessions.length} terminal${teamSessions.length !== 1 ? "s" : ""}`
            : `${teamSessions.length}/${capacity}`}
        </span>
      </div>

      {focusedSessionID && teamSessions.some((s) => s.sessionID === focusedSessionID) ? (
        renderFocusedMode()
      ) : isCustomLayout(team.grid_layout) ? (
        <div className="terminal-grid-content">
          {renderCustomMode()}
        </div>
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
