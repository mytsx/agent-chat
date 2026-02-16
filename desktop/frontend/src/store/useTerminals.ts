import { create } from "zustand";
import { TerminalSession } from "../lib/types";
import {
  CreateTerminal,
  CloseTerminal,
  ResizeTerminal,
  WriteToTerminal,
} from "../../wailsjs/go/main/App";

interface TerminalsState {
  sessions: Record<string, TerminalSession[]>; // teamID -> sessions
  focusedSessionID: string | null;
  setFocusedSession: (id: string | null) => void;
  toggleFocusSession: (id: string) => void;
  addTerminal: (
    teamID: string,
    agentName: string,
    workDir: string
  ) => Promise<string>;
  removeTerminal: (teamID: string, sessionID: string) => Promise<void>;
  removeAllForTeam: (teamID: string) => Promise<void>;
  writeToTerminal: (sessionID: string, data: string) => Promise<void>;
  resizeTerminal: (
    sessionID: string,
    cols: number,
    rows: number
  ) => Promise<void>;
  getTeamSessions: (teamID: string) => TerminalSession[];
}

export const useTerminals = create<TerminalsState>((set, get) => ({
  sessions: {},
  focusedSessionID: null,

  setFocusedSession: (id) => set({ focusedSessionID: id }),

  toggleFocusSession: (id) =>
    set((s) => ({
      focusedSessionID: s.focusedSessionID === id ? null : id,
    })),

  addTerminal: async (teamID, agentName, workDir) => {
    const sessionID = await CreateTerminal(teamID, agentName, workDir);
    const session: TerminalSession = {
      sessionID,
      teamID,
      agentName,
      index: (get().sessions[teamID]?.length ?? 0),
    };

    set((s) => ({
      sessions: {
        ...s.sessions,
        [teamID]: [...(s.sessions[teamID] ?? []), session],
      },
    }));

    return sessionID;
  },

  removeTerminal: async (teamID, sessionID) => {
    await CloseTerminal(sessionID);
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
      } catch {
        // ignore
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

  resizeTerminal: async (sessionID, cols, rows) => {
    await ResizeTerminal(sessionID, cols, rows);
  },

  getTeamSessions: (teamID) => {
    return get().sessions[teamID] ?? [];
  },
}));
