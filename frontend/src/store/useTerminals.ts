import { create } from "zustand";
import { CLIInfo, CLIType, TerminalSession } from "../lib/types";
import { main } from "../../wailsjs/go/models";
import { useTeams } from "./useTeams";
import {
  CreateTerminal,
  CloseTerminal,
  RestartTerminal,
  ResumeTerminal,
  ResizeTerminal,
  WriteToTerminal,
  BroadcastToTeam,
  DetectCLIs,
  OpenTeamFromConfig,
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
  openTeamFromConfig: (teamID: string) => Promise<main.OpenTeamResult[]>;
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
      const pendingID = s.pendingCLISessionIDs[sessionID];
      const session: TerminalSession = {
        sessionID,
        teamID,
        agentName,
        cliType,
        index: currentSessions.length,
        slotIndex: resolvedSlotIndex,
        ...(pendingID !== undefined ? { cliSessionID: pendingID } : {}),
      };
      const pending = { ...s.pendingCLISessionIDs };
      delete pending[sessionID];
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

  openTeamFromConfig: async (teamID) => {
    const results = await OpenTeamFromConfig(teamID);
    const existing = get().sessions[teamID] ?? [];
    const newSessions: TerminalSession[] = [];

    for (const row of results) {
      if (row.error || !row.sessionID) continue;
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
        const pending = { ...s.pendingCLISessionIDs };
        const applied = newSessions.map((sess) => {
          const pid = pending[sess.sessionID];
          if (pid === undefined) return sess;
          delete pending[sess.sessionID];
          return { ...sess, cliSessionID: pid };
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
      console.error("[openTeamFromConfig] refreshTeam failed:", e);
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

    // Replace old session with new one, preserving slotIndex. Clear cliSessionID:
    // the new PTY hasn't captured a session id yet, so a stale id must not keep the
    // resume button enabled — ResumeTerminal(newSessionID) would only fall back to
    // another fresh restart, discarding the just-started process (#40, Codex P3).
    set((s) => ({
      sessions: {
        ...s.sessions,
        [teamID]: (s.sessions[teamID] ?? []).map((t) =>
          t.sessionID === sessionID
            ? { ...t, sessionID: newSessionID, cliSessionID: undefined }
            : t
        ),
      },
    }));

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

    // Replace old session with new one, preserving slotIndex; clear cliSessionID
    // since the resumed session hasn't captured a new id yet.
    set((s) => ({
      sessions: {
        ...s.sessions,
        [teamID]: (s.sessions[teamID] ?? []).map((t) =>
          t.sessionID === sessionID
            ? { ...t, sessionID: newSessionID, cliSessionID: undefined }
            : t
        ),
      },
    }));

    return newSessionID;
  },

  setCLISessionID: (sessionID, cliSessionID) => {
    set((state) => {
      let matched = false;
      const next: Record<string, TerminalSession[]> = {};
      for (const [tid, list] of Object.entries(state.sessions)) {
        next[tid] = list.map((s) => {
          if (s.sessionID === sessionID) {
            matched = true;
            return { ...s, cliSessionID };
          }
          return s;
        });
      }
      if (matched) {
        // Applied directly; also drop any stale buffered id for this session.
        if (state.pendingCLISessionIDs[sessionID] === undefined) {
          return { sessions: next };
        }
        const pending = { ...state.pendingCLISessionIDs };
        delete pending[sessionID];
        return { sessions: next, pendingCLISessionIDs: pending };
      }
      // The session row isn't in the store yet — buffer the id and apply it when
      // the session is added (addTerminal / openTeamFromConfig) (#40, Codex P2).
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
}));
