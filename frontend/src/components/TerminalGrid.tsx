import React, { useEffect, useState } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle } from "react-resizable-panels";
import GridLayout, { WidthProvider, type LayoutItem } from "react-grid-layout/legacy";
import { useTeams } from "../store/useTeams";
import { useTerminals } from "../store/useTerminals";
import { parseGrid, gridCapacity, isCustomLayout, TerminalSession } from "../lib/types";
import TerminalPane from "./TerminalPane";
import SetupWizard from "./SetupWizard";
import GridSelector from "./GridSelector";
import RoomSummaryModal from "./RoomSummaryModal";
import OpenTeamModal from "./OpenTeamModal";
import { useSummaries, useSummaryFor } from "../store/useSummaries";
import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";

type GridSlot =
  | { type: "terminal"; session: TerminalSession }
  | { type: "wizard"; slotIndex: number };

const FreeformGrid = WidthProvider(GridLayout);
const FREEFORM_LAYOUT_STORAGE_KEY = "agent-chat:custom-layouts:v1";
const FREEFORM_COLS = 12;
const FREEFORM_DEFAULT_W = 4;
const FREEFORM_DEFAULT_H = 8;
const FREEFORM_MIN_W = 2;
const FREEFORM_MIN_H = 4;
const FREEFORM_ROW_HEIGHT = 34;

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

function normalizeLayout(item: LayoutItem): LayoutItem {
  const w = clamp(Math.round(item.w || FREEFORM_DEFAULT_W), FREEFORM_MIN_W, FREEFORM_COLS);
  const x = clamp(Math.round(item.x || 0), 0, FREEFORM_COLS - w);
  return {
    ...item,
    x,
    y: Math.max(0, Math.round(item.y || 0)),
    w,
    h: Math.max(FREEFORM_MIN_H, Math.round(item.h || FREEFORM_DEFAULT_H)),
    minW: FREEFORM_MIN_W,
    minH: FREEFORM_MIN_H,
  };
}

function createDefaultLayoutItem(order: number, sessionID: string): LayoutItem {
  const perRow = Math.max(1, Math.floor(FREEFORM_COLS / FREEFORM_DEFAULT_W));
  return normalizeLayout({
    i: sessionID,
    x: (order % perRow) * FREEFORM_DEFAULT_W,
    y: Math.floor(order / perRow) * FREEFORM_DEFAULT_H,
    w: FREEFORM_DEFAULT_W,
    h: FREEFORM_DEFAULT_H,
    minW: FREEFORM_MIN_W,
    minH: FREEFORM_MIN_H,
  });
}

function layoutSignature(layout: readonly LayoutItem[]): string {
  return layout
    .map((item) => normalizeLayout(item))
    .sort((a, b) => a.i.localeCompare(b.i))
    .map((item) => `${item.i}:${item.x}:${item.y}:${item.w}:${item.h}`)
    .join("|");
}

function layoutsEqual(a: readonly LayoutItem[], b: readonly LayoutItem[]): boolean {
  return layoutSignature(a) === layoutSignature(b);
}

function syncLayoutWithSessions(layout: LayoutItem[], sessions: TerminalSession[]): LayoutItem[] {
  const sessionIDs = new Set(sessions.map((s) => s.sessionID));
  const existing = layout.filter((item) => sessionIDs.has(item.i)).map(normalizeLayout);
  const existingIDs = new Set(existing.map((item) => item.i));
  let order = existing.length;

  for (const session of sessions) {
    if (!existingIDs.has(session.sessionID)) {
      existing.push(createDefaultLayoutItem(order, session.sessionID));
      order++;
    }
  }

  return existing;
}

function readSavedCustomLayouts(): Record<string, LayoutItem[]> {
  if (typeof window === "undefined") {
    return {};
  }
  try {
    const raw = window.localStorage.getItem(FREEFORM_LAYOUT_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw) as Record<string, LayoutItem[]>;
    const result: Record<string, LayoutItem[]> = {};

    for (const [teamID, layout] of Object.entries(parsed)) {
      if (!Array.isArray(layout)) {
        continue;
      }
      result[teamID] = layout
        .filter((item): item is LayoutItem => !!item && typeof item.i === "string")
        .map((item) => normalizeLayout(item));
    }

    return result;
  } catch {
    return {};
  }
}

export default function TerminalGrid() {
  const { teams, activeTeamID, updateTeam } = useTeams();
  const { sessions, focusedSessionID, toggleFocusSession, setFocusedSession, loadCLIs, removeTerminal, restartTerminal, resumeTerminal } = useTerminals();
  const [showCustomSetup, setShowCustomSetup] = useState(false);
  const [openModal, setOpenModal] = useState(false);
  const [showSummary, setShowSummary] = useState(false);
  const loadSummary = useSummaries((s) => s.loadSummary);
  const [customLayouts, setCustomLayouts] = useState<Record<string, LayoutItem[]>>(() =>
    readSavedCustomLayouts()
  );

  const team = teams.find((t) => t.id === activeTeamID);
  const currentGridLayout = team?.grid_layout ?? "1x1";
  const teamSessions = team ? (sessions[team.id] ?? []) : [];
  const roomName = team?.name ?? "";
  const roomSummary = useSummaryFor(roomName);
  // Load the room's saved summary so the "continue" panel can surface it (#29).
  useEffect(() => {
    if (roomName) loadSummary(roomName);
  }, [roomName, loadSummary]);
  const { cols, rows } = parseGrid(currentGridLayout);
  const capacity = gridCapacity(currentGridLayout);
  const isCustomMode = isCustomLayout(currentGridLayout);
  const customLayout = team ? (customLayouts[team.id] ?? []) : [];

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

  useEffect(() => {
    if (!team || !isCustomMode) return;
    setCustomLayouts((prev) => {
      const current = prev[team.id] ?? [];
      const synced = syncLayoutWithSessions(current, teamSessions);
      if (layoutsEqual(current, synced)) {
        return prev;
      }
      return {
        ...prev,
        [team.id]: synced,
      };
    });
  }, [team, isCustomMode, teamSessions]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    try {
      window.localStorage.setItem(FREEFORM_LAYOUT_STORAGE_KEY, JSON.stringify(customLayouts));
    } catch {
      // no-op: localStorage can fail in restricted environments
    }
  }, [customLayouts]);

  useEffect(() => {
    if (!isCustomMode) {
      setShowCustomSetup(false);
    }
  }, [isCustomMode]);

  const handleLayoutChange = async (layout: string) => {
    if (!team) return;
    await updateTeam(team.id, team.name, layout, team.agents);
  };

  const handleCustomLayoutChange = (layout: readonly LayoutItem[]) => {
    if (!team) return;
    setCustomLayouts((prev) => {
      const current = prev[team.id] ?? [];
      if (layoutsEqual(current, layout)) {
        return prev;
      }
      return {
        ...prev,
        [team.id]: layout.map((item) => normalizeLayout(item)),
      };
    });
  };

  if (!team) return <div className="terminal-grid-empty">No team selected</div>;

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

  const renderTerminalPane = (
    session: TerminalSession,
    options: { isFocused?: boolean; onRemove?: () => void; teamID?: string } = {}
  ) => {
    const actionTeamID = options.teamID ?? session.teamID;
    return (
      <TerminalPane
        sessionID={session.sessionID}
        agentName={session.agentName}
        cliType={session.cliType}
        isFocused={options.isFocused ?? false}
        onToggleFocus={() => toggleFocusSession(session.sessionID)}
        onRemove={options.onRemove}
        onRestart={() =>
          restartTerminal(actionTeamID, session.sessionID).catch((err) =>
            console.error("[restart] failed:", err)
          )
        }
        onResume={() =>
          resumeTerminal(actionTeamID, session.sessionID).catch((err) =>
            console.error("[resume] failed:", err)
          )
        }
        canResume={!!session.cliSessionID}
      />
    );
  };

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
            {renderTerminalPane(s, { isFocused: s.sessionID === focusedSessionID })}
          </div>
        ))}
      </div>
    );
  };

  const renderSlot = (slot: GridSlot) => {
    if (slot.type === "terminal") {
      return renderTerminalPane(slot.session);
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

    if (teamSessions.length === 0) {
      return (
        <div className="custom-layout-shell">
          <div className="custom-layout-empty">
            <SetupWizard
              slotIndex={nextSlotIndex}
              teamID={team.id}
              onCreated={() => setShowCustomSetup(false)}
            />
          </div>
        </div>
      );
    }

    return (
      <div className="custom-layout-shell">
        <div className="custom-layout-canvas">
          <FreeformGrid
            className="custom-grid-layout"
            layout={customLayout}
            cols={FREEFORM_COLS}
            rowHeight={FREEFORM_ROW_HEIGHT}
            margin={[8, 8]}
            containerPadding={[8, 8]}
            compactType={null}
            preventCollision={false}
            draggableHandle=".terminal-header"
            draggableCancel=".terminal-header-actions,.terminal-header-actions *,button,input,textarea,select,a"
            resizeHandles={["se", "s", "e"]}
            onLayoutChange={handleCustomLayoutChange}
          >
            {teamSessions.map((s) => (
              <div key={s.sessionID} className="custom-grid-item">
                {renderTerminalPane(s, {
                  teamID: team.id,
                  onRemove: () =>
                    removeTerminal(team.id, s.sessionID).catch((err) =>
                      console.error("[remove] failed:", err)
                    ),
                })}
              </div>
            ))}
          </FreeformGrid>
        </div>
        {showCustomSetup && (
          <div className="custom-setup-drawer">
            <SetupWizard
              slotIndex={nextSlotIndex}
              teamID={team.id}
              onCreated={() => setShowCustomSetup(false)}
            />
          </div>
        )}
      </div>
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
        <div className="terminal-grid-actions">
          {(team.agents?.length ?? 0) > 0 && teamSessions.length === 0 && (
            <button
              type="button"
              className="btn-sm"
              onClick={() => setOpenModal(true)}
              title="Kayıtlı agent yapılandırmasıyla tüm terminalleri aç"
            >
              Config ile Aç
            </button>
          )}
          {(team.agents?.length ?? 0) > 0 && teamSessions.length === 0 && roomSummary?.exists && (
            <button
              type="button"
              className="btn-sm"
              onClick={() => setShowSummary(true)}
              title="Önceki session'ın özeti — devam eden agent'lara verilecek bağlam"
            >
              📝 Önceki Özet
            </button>
          )}
          {isCustomMode && (
            <button
              type="button"
              className="btn-sm"
              onClick={() => setShowCustomSetup((prev) => !prev)}
            >
              {showCustomSetup ? "Hide Setup" : "Add Terminal"}
            </button>
          )}
          <span className="terminal-count">
            {isCustomMode
              ? `${teamSessions.length} terminal${teamSessions.length !== 1 ? "s" : ""}`
              : `${teamSessions.length}/${capacity}`}
          </span>
        </div>
      </div>

      {focusedSessionID && teamSessions.some((s) => s.sessionID === focusedSessionID) ? (
        renderFocusedMode()
      ) : isCustomMode ? (
        <div className="terminal-grid-content">
          {renderCustomMode()}
        </div>
      ) : (
        <div className="terminal-grid-content">
          {renderResizablePanels()}
        </div>
      )}

      {showSummary && roomName && (
        <RoomSummaryModal room={roomName} onClose={() => setShowSummary(false)} />
      )}
      {openModal && team && (
        <OpenTeamModal teamID={team.id} onClose={() => setOpenModal(false)} />
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
