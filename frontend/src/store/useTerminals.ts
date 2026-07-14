import { create, type StoreApi } from "zustand";
import { CLIInfo, CLIType, TerminalSession, SessionInfo } from "../lib/types";
import { focusAfterSessionRemove, focusAfterSessionReplace } from "../lib/terminalFocus";
import { createSingleFlight } from "../lib/singleFlight";
import { main } from "../../wailsjs/go/models";
import { useTeams } from "./useTeams";
import { useUsage } from "./useUsage";
import {
  CreateTerminal,
  CreateTerminalResume,
  ListKnownAgents,
  ListAgentSessions,
  CloseTerminal,
  RestartTerminal,
  ResumeTerminal,
  SwitchTerminal,
  ResizeTerminal,
  WriteToTerminal,
  BroadcastToTeam,
  DetectCLIs,
  OpenTeamFromConfigResume,
} from "../../wailsjs/go/main/App";

interface TerminalsState {
  sessions: Record<string, TerminalSession[]>; // teamID -> sessions
  // pendingCLISessionIDs buffers captured CLI session ids whose terminal:resume-
  // available event arrived BEFORE the session row was inserted (e.g.
  // openTeamFromConfig awaits all backend creates before adding any rows). The id
  // is applied when the matching session is added, so the "Devam Et" button isn't
  // left disabled for a resumable terminal (#40, Codex P2). Keyed by sessionID.
  pendingCLISessionIDs: Record<string, string>;
  focusedSessionID: string | null;
  availableCLIs: CLIInfo[];
  setFocusedSession: (id: string | null) => void;
  toggleFocusSession: (id: string) => void;
  loadCLIs: () => Promise<void>;
  addTerminal: (
    teamID: string,
    agentName: string,
    workDir: string,
    cliType: CLIType,
    promptId?: string,
    slotIndex?: number,
    useWorktree?: boolean
  ) => Promise<string>;
  openTeamFromConfigResume: (teamID: string, resumeIDs: Record<string, string>) => Promise<main.OpenTeamResult[]>;
  removeTerminal: (teamID: string, sessionID: string) => Promise<void>;
  removeAllForTeam: (teamID: string) => Promise<void>;
  writeToTerminal: (sessionID: string, data: string) => Promise<void>;
  broadcastToTeam: (
    teamID: string,
    text: string,
    submit: boolean
  ) => Promise<void>;
  resizeTerminal: (
    sessionID: string,
    cols: number,
    rows: number
  ) => Promise<void>;
  restartTerminal: (teamID: string, sessionID: string) => Promise<string>;
  resumeTerminal: (teamID: string, sessionID: string) => Promise<string>;
  switchTerminal: (teamID: string, sessionID: string, targetCLI: CLIType) => Promise<string>;
  setCLISessionID: (sessionID: string, cliSessionID: string) => void;
  getTeamSessions: (teamID: string) => TerminalSession[];
  createTerminalResume: (teamID: string, agentName: string, workDir: string, cliType: CLIType, promptId: string, slotIndex: number, useWorktree: boolean, resumeID: string) => Promise<string>;
  listKnownAgents: (teamID: string) => Promise<string[]>;
  listAgentSessions: (teamID: string, agentName: string) => Promise<SessionInfo[]>;
}

type GetTerminalsState = StoreApi<TerminalsState>["getState"];
type SetTerminalsState = StoreApi<TerminalsState>["setState"];
type TerminalLifecycleAction = "restartTerminal" | "resumeTerminal" | "switch";

// restart/resume both replace the same old sessionID, and each backend call
// spawns a fresh PTY. A rapid double-trigger (double-click, or restart then
// resume) would start two PTYs while the store keeps only the first
// replacement's new id, orphaning the second PTY. Keying the replace on the old
// sessionID coalesces overlapping calls into a single backend replace; the
// second caller awaits and returns the first call's new sessionID. Keyed by the
// old sessionID (the value shared by racing callers).
const replaceInFlight = createSingleFlight<string>();

function drainPendingCLISessionID(
  pendingBySession: Record<string, string>,
  sessionID: string
): [string | undefined, Record<string, string>] {
  const cliSessionID = pendingBySession[sessionID];
  if (cliSessionID === undefined) {
    return [undefined, pendingBySession];
  }
  const next = { ...pendingBySession };
  delete next[sessionID];
  return [cliSessionID, next];
}

function attachPendingCLISessionID(
  session: TerminalSession,
  pendingBySession: Record<string, string>
): [TerminalSession, Record<string, string>] {
  const [cliSessionID, pending] = drainPendingCLISessionID(pendingBySession, session.sessionID);
  return [cliSessionID === undefined ? session : { ...session, cliSessionID }, pending];
}

function replaceSessionID(
  list: TerminalSession[],
  oldSessionID: string,
  newSessionID: string,
  cliSessionID: string | undefined
): TerminalSession[] {
  return list.map((terminal) =>
    terminal.sessionID === oldSessionID
      ? { ...terminal, sessionID: newSessionID, cliSessionID }
      : terminal
  );
}

function setTeamSessions(
  sessionsByTeam: Record<string, TerminalSession[]>,
  teamID: string,
  sessions: TerminalSession[]
): Record<string, TerminalSession[]> {
  return {
    ...sessionsByTeam,
    [teamID]: sessions,
  };
}

async function refreshTeamAfterTerminalChange(teamID: string, logPrefix: string): Promise<void> {
  try {
    await useTeams.getState().refreshTeam(teamID);
  } catch (e) {
    console.error(logPrefix, e);
  }
}

function requireExistingSession(
  sessions: TerminalSession[],
  teamID: string,
  sessionID: string,
  action: TerminalLifecycleAction
): void {
  if (sessions.some((session) => session.sessionID === sessionID)) {
    return;
  }
  console.error(`[${action}] session ${sessionID} not found in team ${teamID}`);
  throw new Error("Session not found");
}

function replaceRestartedSessionState(
  state: TerminalsState,
  teamID: string,
  oldSessionID: string,
  newSessionID: string
): Partial<TerminalsState> {
  const [captured, pending] = drainPendingCLISessionID(state.pendingCLISessionIDs, newSessionID);
  return {
    sessions: setTeamSessions(
      state.sessions,
      teamID,
      replaceSessionID(
        state.sessions[teamID] ?? [],
        oldSessionID,
        newSessionID,
        captured
      )
    ),
    pendingCLISessionIDs: pending,
    focusedSessionID: focusAfterSessionReplace(
      state.focusedSessionID,
      oldSessionID,
      newSessionID
    ),
  };
}

async function replaceTerminalFromBackend(
  get: GetTerminalsState,
  set: SetTerminalsState,
  teamID: string,
  sessionID: string,
  action: TerminalLifecycleAction,
  replaceBackendSession: (sessionID: string) => Promise<string>
): Promise<string> {
  const teamSessions = get().sessions[teamID] ?? [];
  requireExistingSession(teamSessions, teamID, sessionID, action);

  const newSessionID = await replaceBackendSession(sessionID);
  set((s) => replaceRestartedSessionState(s, teamID, sessionID, newSessionID));

  // If the terminal was closed (removeTerminal) while the backend replace was in
  // flight, the old sessionID is already gone from the store, so replaceSessionID's
  // map() couldn't graft the new id in — the freshly-spawned backend PTY would leak.
  // The user closed the terminal, so the new PTY should not survive: close it. Runs
  // once per replace (single-flight coalesces callers), so no double-close (Gemini
  // PR #77 high).
  const tracked = (get().sessions[teamID] ?? []).some((t) => t.sessionID === newSessionID);
  if (!tracked) {
    CloseTerminal(newSessionID).catch((e) =>
      console.error(`[${action}] orphaned PTY temizlenemedi (${newSessionID}):`, e)
    );
  }

  return newSessionID;
}

function attachPendingCLISessionIDs(
  sessions: TerminalSession[],
  pendingBySession: Record<string, string>
): [TerminalSession[], Record<string, string>] {
  let pending = pendingBySession;
  const attached = sessions.map((session) => {
    const [nextSession, nextPending] = attachPendingCLISessionID(session, pending);
    pending = nextPending;
    return nextSession;
  });
  return [attached, pending];
}

function appendSessionState(
  state: TerminalsState,
  session: TerminalSession
): Partial<TerminalsState> {
  const [[attached], pending] = attachPendingCLISessionIDs(
    [session],
    state.pendingCLISessionIDs
  );
  return {
    sessions: setTeamSessions(state.sessions, session.teamID, [
      ...(state.sessions[session.teamID] ?? []),
      attached,
    ]),
    pendingCLISessionIDs: pending,
  };
}

export const useTerminals = create<TerminalsState>((set, get) => ({
  sessions: {},
  pendingCLISessionIDs: {},
  focusedSessionID: null,
  availableCLIs: [],

  setFocusedSession: (id) => set({ focusedSessionID: id }),

  toggleFocusSession: (id) =>
    set((s) => ({
      focusedSessionID: s.focusedSessionID === id ? null : id,
    })),

  loadCLIs: async () => {
    try {
      const clis = await DetectCLIs();
      set({ availableCLIs: clis as unknown as CLIInfo[] });
    } catch (e) {
      if (import.meta.env.DEV) console.warn("CLI detection failed:", e);
    }
  },

  addTerminal: async (teamID, agentName, workDir, cliType, promptId, slotIndex, useWorktree = false) => {
    const currentSessions = get().sessions[teamID] ?? [];
    const resolvedSlotIndex = slotIndex ?? currentSessions.length;
    const sessionID = await CreateTerminal(
      teamID,
      agentName,
      workDir,
      cliType,
      promptId ?? "",
      useWorktree,
      resolvedSlotIndex
    );
    set((s) => appendSessionState(s, {
      sessionID,
      teamID,
      agentName,
      cliType,
      index: currentSessions.length,
      slotIndex: resolvedSlotIndex,
    }));

    // Backend persisted this agent into the team via UpsertAgent; re-pull the
    // team so later grid updates don't echo a stale agents array. The PTY is
    // already running, so a refresh failure must not fail the whole creation.
    await refreshTeamAfterTerminalChange(teamID, "[addTerminal] refreshTeam failed:");

    return sessionID;
  },

  openTeamFromConfigResume: async (teamID, resumeIDs) => {
    const results = await OpenTeamFromConfigResume(teamID, resumeIDs);
    const existing = get().sessions[teamID] ?? [];
    // The backend is idempotent per slot: a retry / overlapping batch returns the
    // EXISTING session id for an already-open slot instead of spawning a new PTY. Skip
    // ids already in the store so we don't append a duplicate row for one PTY (Codex P2).
    const known = new Set(existing.map((s) => s.sessionID));
    const newSessions: TerminalSession[] = [];

    for (const row of results) {
      if (row.error || !row.sessionID || known.has(row.sessionID)) continue;
      known.add(row.sessionID);
      newSessions.push({
        sessionID: row.sessionID,
        teamID,
        agentName: row.agentName,
        cliType: (row.cliType || "shell") as CLIType,
        index: existing.length + newSessions.length,
        slotIndex: row.slotIndex,
      });
    }

    if (newSessions.length > 0) {
      set((s) => {
        // Drain buffered resume ids whose event beat the row insertion: this batch
        // path awaits every backend create before adding any rows, so a fast CLI's
        // captured id can arrive first (#40, Codex P2).
        const [applied, pending] = attachPendingCLISessionIDs(
          newSessions,
          s.pendingCLISessionIDs
        );
        return {
          sessions: setTeamSessions(s.sessions, teamID, [
            ...(s.sessions[teamID] ?? []),
            ...applied,
          ]),
          pendingCLISessionIDs: pending,
        };
      });
    }

    // OpenTeamFromConfig re-persists each agent (CreateTerminal → UpsertAgent),
    // possibly migrating legacy slot indices; refresh so the team store matches.
    // PTYs are already running, so a refresh failure must not fail the batch.
    await refreshTeamAfterTerminalChange(teamID, "[openTeamFromConfigResume] refreshTeam failed:");

    return results;
  },

  removeTerminal: async (teamID, sessionID) => {
    try {
      await CloseTerminal(sessionID);
    } catch (e) {
      const msg = String(e).toLowerCase();
      // Backend side can already have evicted a dead session.
      if (!msg.includes("session not found")) {
        throw e;
      }
    }
    set((s) => ({
      sessions: setTeamSessions(
        s.sessions,
        teamID,
        (s.sessions[teamID] ?? []).filter((t) => t.sessionID !== sessionID)
      ),
      focusedSessionID: focusAfterSessionRemove(s.focusedSessionID, [sessionID]),
    }));
    // Drop the closed session's cached usage so entries/limitHits don't grow
    // unbounded and the usage panel doesn't show a stale row (FIX 2).
    useUsage.getState().clear(sessionID);
  },

  removeAllForTeam: async (teamID) => {
    const teamSessions = get().sessions[teamID] ?? [];
    for (const s of teamSessions) {
      try {
        await CloseTerminal(s.sessionID);
      } catch (e) {
        if (import.meta.env.DEV) console.warn("CloseTerminal failed:", e);
      }
    }
    set((s) => {
      const removedSessionIDs = (s.sessions[teamID] ?? []).map((session) => session.sessionID);
      const sessions = { ...s.sessions };
      delete sessions[teamID];
      return {
        sessions,
        focusedSessionID: focusAfterSessionRemove(s.focusedSessionID, removedSessionIDs),
      };
    });
    // Clear cached usage for every removed session (FIX 2).
    for (const s of teamSessions) {
      useUsage.getState().clear(s.sessionID);
    }
  },

  writeToTerminal: async (sessionID, data) => {
    await WriteToTerminal(sessionID, data);
  },

  // Fan-out the same text into every agent terminal of a team at once (raw PTY
  // write, not a chat message). The backend handles per-cliType injection,
  // observer exclusion, and per-session error tolerance; errors here are only
  // preconditions (blank text / no open terminals) and propagate to the caller.
  broadcastToTeam: async (teamID, text, submit) => {
    await BroadcastToTeam(teamID, text, submit);
  },

  resizeTerminal: async (sessionID, cols, rows) => {
    await ResizeTerminal(sessionID, cols, rows);
  },

  restartTerminal: async (teamID, sessionID) => {
    // Replace old session with new one, preserving slotIndex. cliSessionID resets
    // to whatever was buffered for newSessionID (normally undefined → resume button
    // disabled until the new PTY captures an id; a stale old id must NOT carry over,
    // or ResumeTerminal would fall back to another fresh restart — #40 Codex P3).
    // Draining pendingCLISessionIDs here keeps all insertion paths consistent so the
    // captured id is never lost if its event raced this replacement (#40 Codex P2).
    // Coalesce overlapping replaces on this sessionID so a double-trigger can't
    // orphan a second backend PTY (Gemini PR #76).
    return replaceInFlight.run(sessionID, () =>
      replaceTerminalFromBackend(get, set, teamID, sessionID, "restartTerminal", RestartTerminal)
    );
  },

  resumeTerminal: async (teamID, sessionID) => {
    // Replace old session with new one, preserving slotIndex. cliSessionID resets to
    // whatever was buffered for newSessionID (normally undefined until the resumed
    // PTY captures its new id; the resumed CLI regenerates its session id). Draining
    // pendingCLISessionIDs keeps all insertion paths consistent so a captured id
    // isn't lost if its event raced this replacement (#40 Codex P2).
    // Coalesce overlapping replaces on this sessionID so a double-trigger can't
    // orphan a second backend PTY (Gemini PR #76).
    return replaceInFlight.run(sessionID, () =>
      replaceTerminalFromBackend(get, set, teamID, sessionID, "resumeTerminal", ResumeTerminal)
    );
  },

  switchTerminal: async (teamID, sessionID, targetCLI) => {
    // SwitchTerminal CLOSES the old PTY and spawns a fresh one under a new UUID
    // with a DIFFERENT cliType, so it's a replace like restart/resume — graft the
    // new sessionID into the slot (and close it if the slot was removed mid-flight).
    // Coalesce overlapping switches on this sessionID so a double-trigger can't
    // orphan a second backend PTY (matching restart/resume, Gemini PR #76).
    return replaceInFlight.run(sessionID, async () => {
      const newSessionID = await replaceTerminalFromBackend(
        get,
        set,
        teamID,
        sessionID,
        "switch",
        (sid) => SwitchTerminal(sid, targetCLI)
      );
      // KEY DIFFERENCE from restart/resume: replaceSessionID preserves the OLD
      // cliType, but a switch changes it. Overwrite the grafted row's cliType to
      // the target CLI so the badge and switch dialog reflect the new agent. A
      // no-op if the slot was closed mid-flight (newSessionID isn't tracked).
      set((s) => ({
        sessions: setTeamSessions(
          s.sessions,
          teamID,
          (s.sessions[teamID] ?? []).map((t) =>
            t.sessionID === newSessionID ? { ...t, cliType: targetCLI } : t
          )
        ),
      }));
      // The old sessionID's PTY is gone; drop its cached usage snapshot/limit-hit so
      // the usage panel doesn't keep a stale row for a dead session (FIX 2).
      useUsage.getState().clear(sessionID);
      return newSessionID;
    });
  },

  setCLISessionID: (sessionID, cliSessionID) => {
    set((state) => {
      for (const [teamID, list] of Object.entries(state.sessions)) {
        const index = list.findIndex((s) => s.sessionID === sessionID);
        if (index === -1) {
          continue;
        }

        const [, pending] = drainPendingCLISessionID(state.pendingCLISessionIDs, sessionID);
        if (list[index].cliSessionID === cliSessionID && pending === state.pendingCLISessionIDs) {
          return state;
        }

        const nextList = [...list];
        nextList[index] = { ...nextList[index], cliSessionID };
        return {
          sessions: setTeamSessions(state.sessions, teamID, nextList),
          pendingCLISessionIDs: pending,
        };
      }

      // The session row isn't in the store yet — buffer the id and apply it when
      // the session is added (addTerminal / openTeamFromConfig) (#40, Codex P2).
      if (state.pendingCLISessionIDs[sessionID] === cliSessionID) {
        return state;
      }
      return {
        pendingCLISessionIDs: {
          ...state.pendingCLISessionIDs,
          [sessionID]: cliSessionID,
        },
      };
    });
  },

  getTeamSessions: (teamID) => {
    return get().sessions[teamID] ?? [];
  },

  createTerminalResume: async (teamID, agentName, workDir, cliType, promptId, slotIndex, useWorktree, resumeID) => {
    const currentSessions = get().sessions[teamID] ?? [];
    const sessionID = await CreateTerminalResume(teamID, agentName, workDir, cliType, promptId, useWorktree, slotIndex, resumeID);
    set((s) => appendSessionState(s, {
      sessionID,
      teamID,
      agentName,
      cliType,
      index: currentSessions.length,
      slotIndex,
    }));
    await refreshTeamAfterTerminalChange(teamID, "[createTerminalResume] refreshTeam:");
    return sessionID;
  },

  listKnownAgents: async (teamID) => {
    try {
      return await ListKnownAgents(teamID);
    } catch (e) {
      console.error("[listKnownAgents]", e);
      return [];
    }
  },

  listAgentSessions: async (teamID, agentName) => {
    try {
      return (await ListAgentSessions(teamID, agentName)) as unknown as SessionInfo[];
    } catch (e) {
      console.error("[listAgentSessions]", e);
      return [];
    }
  },
}));
