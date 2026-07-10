import { create } from "zustand";
import { CLIInfo, CLIType, TerminalSession, SessionInfo } from "../lib/types";
import { main } from "../../wailsjs/go/models";
import { useTeams } from "./useTeams";
import {
  CreateTerminal,
  CreateTerminalResume,
  ListKnownAgents,
  ListAgentSessions,
  CloseTerminal,
  RestartTerminal,
  ResumeTerminal,
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
  setCLISessionID: (sessionID: string, cliSessionID: string) => void;
  getTeamSessions: (teamID: string) => TerminalSession[];
  createTerminalResume: (teamID: string, agentName: string, workDir: string, cliType: CLIType, promptId: string, slotIndex: number, useWorktree: boolean, resumeID: string) => Promise<string>;
  listKnownAgents: (teamID: string) => Promise<string[]>;
  listAgentSessions: (teamID: string, agentName: string) => Promise<SessionInfo[]>;
}

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
    set((s) => {
      // Apply any resume id captured before this row existed (#40, Codex P2).
      const [session, pending] = attachPendingCLISessionID({
        sessionID,
        teamID,
        agentName,
        cliType,
        index: currentSessions.length,
        slotIndex: resolvedSlotIndex,
      }, s.pendingCLISessionIDs);
      return {
        sessions: {
          ...s.sessions,
          [teamID]: [...(s.sessions[teamID] ?? []), session],
        },
        pendingCLISessionIDs: pending,
      };
    });

    // Backend persisted this agent into the team via UpsertAgent; re-pull the
    // team so later grid updates don't echo a stale agents array. The PTY is
    // already running, so a refresh failure must not fail the whole creation.
    try {
      await useTeams.getState().refreshTeam(teamID);
    } catch (e) {
      console.error("[addTerminal] refreshTeam failed:", e);
    }

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
        let pending = s.pendingCLISessionIDs;
        const applied = newSessions.map((sess) => {
          const [session, nextPending] = attachPendingCLISessionID(sess, pending);
          pending = nextPending;
          return session;
        });
        return {
          sessions: {
            ...s.sessions,
            [teamID]: [...(s.sessions[teamID] ?? []), ...applied],
          },
          pendingCLISessionIDs: pending,
        };
      });
    }

    // OpenTeamFromConfig re-persists each agent (CreateTerminal → UpsertAgent),
    // possibly migrating legacy slot indices; refresh so the team store matches.
    // PTYs are already running, so a refresh failure must not fail the batch.
    try {
      await useTeams.getState().refreshTeam(teamID);
    } catch (e) {
      console.error("[openTeamFromConfigResume] refreshTeam failed:", e);
    }

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
      sessions: {
        ...s.sessions,
        [teamID]: (s.sessions[teamID] ?? []).filter(
          (t) => t.sessionID !== sessionID
        ),
      },
    }));
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
      const sessions = { ...s.sessions };
      delete sessions[teamID];
      return { sessions };
    });
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
    const teamSessions = get().sessions[teamID] ?? [];
    const oldSession = teamSessions.find((s) => s.sessionID === sessionID);
    if (!oldSession) {
      console.error(`[restartTerminal] session ${sessionID} not found in team ${teamID}`);
      throw new Error("Session not found");
    }

    const newSessionID = await RestartTerminal(sessionID);

    // Replace old session with new one, preserving slotIndex. cliSessionID resets
    // to whatever was buffered for newSessionID (normally undefined → resume button
    // disabled until the new PTY captures an id; a stale old id must NOT carry over,
    // or ResumeTerminal would fall back to another fresh restart — #40 Codex P3).
    // Draining pendingCLISessionIDs here keeps all insertion paths consistent so the
    // captured id is never lost if its event raced this replacement (#40 Codex P2).
    set((s) => {
      const [captured, pending] = drainPendingCLISessionID(s.pendingCLISessionIDs, newSessionID);
      return {
        sessions: {
          ...s.sessions,
          [teamID]: (s.sessions[teamID] ?? []).map((t) =>
            t.sessionID === sessionID
              ? { ...t, sessionID: newSessionID, cliSessionID: captured }
              : t
          ),
        },
        pendingCLISessionIDs: pending,
      };
    });

    return newSessionID;
  },

  resumeTerminal: async (teamID, sessionID) => {
    const teamSessions = get().sessions[teamID] ?? [];
    const oldSession = teamSessions.find((s) => s.sessionID === sessionID);
    if (!oldSession) {
      console.error(`[resumeTerminal] session ${sessionID} not found in team ${teamID}`);
      throw new Error("Session not found");
    }

    const newSessionID = await ResumeTerminal(sessionID);

    // Replace old session with new one, preserving slotIndex. cliSessionID resets to
    // whatever was buffered for newSessionID (normally undefined until the resumed
    // PTY captures its new id; the resumed CLI regenerates its session id). Draining
    // pendingCLISessionIDs keeps all insertion paths consistent so a captured id
    // isn't lost if its event raced this replacement (#40 Codex P2).
    set((s) => {
      const [captured, pending] = drainPendingCLISessionID(s.pendingCLISessionIDs, newSessionID);
      return {
        sessions: {
          ...s.sessions,
          [teamID]: (s.sessions[teamID] ?? []).map((t) =>
            t.sessionID === sessionID
              ? { ...t, sessionID: newSessionID, cliSessionID: captured }
              : t
          ),
        },
        pendingCLISessionIDs: pending,
      };
    });

    return newSessionID;
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
          sessions: {
            ...state.sessions,
            [teamID]: nextList,
          },
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
    set((s) => {
      const [session, pending] = attachPendingCLISessionID({
        sessionID,
        teamID,
        agentName,
        cliType,
        index: currentSessions.length,
        slotIndex,
      }, s.pendingCLISessionIDs);
      return {
        sessions: {
          ...s.sessions,
          [teamID]: [...(s.sessions[teamID] ?? []), session],
        },
        pendingCLISessionIDs: pending,
      };
    });
    try {
      await useTeams.getState().refreshTeam(teamID);
    } catch (e) {
      console.error("[createTerminalResume] refreshTeam:", e);
    }
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
