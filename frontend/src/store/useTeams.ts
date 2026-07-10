import { create } from "zustand";
import { Team, AgentConfig } from "../lib/types";
import {
  ListTeams,
  GetTeam,
  CreateTeam,
  UpdateTeam,
  DeleteTeam,
  SetTeamManager,
  SetTeamObserver,
  SetCustomPrompt,
  SaveSession,
} from "../../wailsjs/go/main/App";

interface TeamsState {
  teams: Team[];
  activeTeamID: string | null;
  loading: boolean;

  loadTeams: () => Promise<void>;
  setActiveTeam: (id: string) => void;
  createTeam: (
    name: string,
    gridLayout: string,
    agents: AgentConfig[]
  ) => Promise<Team>;
  updateTeam: (
    id: string,
    name: string,
    gridLayout: string,
    agents: AgentConfig[]
  ) => Promise<void>;
  setTeamManager: (id: string, managerAgent: string) => Promise<void>;
  // setTeamObserver marks an agent as the room's read-only observer (#17),
  // mirroring setTeamManager. Persists AgentConfig.Role="observer" so the agent
  // joins as observer (send_message blocked, read_all allowed) and is excluded
  // from broadcasts. Mutually exclusive with manager (backend clears the other).
  setTeamObserver: (id: string, agentName: string) => Promise<void>;
  // setCustomPrompt updates a room's charter (start-of-room context injected into
  // each new agent's startup prompt). Uses the dedicated backend endpoint rather
  // than updateTeam, which omits custom_prompt and would reset it on layout change.
  setCustomPrompt: (id: string, text: string) => Promise<void>;
  // saveSession writes an immutable per-session snapshot of a room (messages +
  // agent roster) to disk via the hub, for later summarization (#29). Returns
  // whether a file was written (false when the room is empty or unchanged since
  // its last snapshot) and the snapshotted message count, so the UI can confirm.
  saveSession: (id: string) => Promise<{ saved: boolean; count: number }>;
  refreshTeam: (id: string) => Promise<void>;
  deleteTeam: (id: string) => Promise<void>;
  // removeTeamLocal drops a team from client state only (no backend call). Used
  // for create-rollback cleanup: deleteTeam filters state only after its backend
  // binding resolves, so if that delete also fails the optimistic tab would linger.
  removeTeamLocal: (id: string) => void;
}

function replaceTeam(teams: Team[], id: string, updated: Team): Team[] {
  return teams.map((team) => (team.id === id ? updated : team));
}

function removeTeam(teams: Team[], id: string): Team[] {
  return teams.filter((team) => team.id !== id);
}

function nextActiveTeamID(
  teams: Team[],
  currentActiveID: string | null,
  removedID: string
): string | null {
  return currentActiveID === removedID ? (teams[0]?.id ?? null) : currentActiveID;
}

function removeTeamState(
  state: TeamsState,
  id: string
): Pick<TeamsState, "teams" | "activeTeamID"> {
  const teams = removeTeam(state.teams, id);
  return {
    teams,
    activeTeamID: nextActiveTeamID(teams, state.activeTeamID, id),
  };
}

export const useTeams = create<TeamsState>((set, get) => ({
  teams: [],
  activeTeamID: null,
  loading: false,

  loadTeams: async () => {
    set({ loading: true });
    try {
      const teams = await ListTeams();
      set({ teams: teams || [], loading: false });
      if (teams && teams.length > 0 && !get().activeTeamID) {
        set({ activeTeamID: teams[0].id });
      }
    } catch (e) {
      if (import.meta.env.DEV) console.warn("Failed to load teams:", e);
      set({ loading: false });
    }
  },

  setActiveTeam: (id: string) => set({ activeTeamID: id }),

  createTeam: async (name, gridLayout, agents) => {
    const t = await CreateTeam(name, gridLayout, agents);
    set((s) => ({
      teams: [...s.teams, t],
      activeTeamID: t.id,
    }));
    return t;
  },

  updateTeam: async (id, name, gridLayout, agents) => {
    const t = await UpdateTeam(id, name, gridLayout, agents);
    set((s) => ({
      teams: replaceTeam(s.teams, id, t),
    }));
  },

  setTeamManager: async (id, managerAgent) => {
    const t = await SetTeamManager(id, managerAgent);
    set((s) => ({
      teams: replaceTeam(s.teams, id, t),
    }));
  },

  setTeamObserver: async (id, agentName) => {
    const t = await SetTeamObserver(id, agentName);
    set((s) => ({
      teams: replaceTeam(s.teams, id, t),
    }));
  },

  setCustomPrompt: async (id, text) => {
    const t = await SetCustomPrompt(id, text);
    set((s) => ({
      teams: replaceTeam(s.teams, id, t),
    }));
  },

  // saveSession does not mutate team state (the snapshot lives on disk under
  // hub-state/sessions/), so it just relays the hub's result to the caller.
  saveSession: async (id) => {
    const r = await SaveSession(id);
    return { saved: r.saved, count: r.count };
  },

  // Re-pull a team from the backend. Needed after the backend persists agents
  // (CreateTerminal/OpenTeamFromConfig → UpsertAgent) so the in-memory team's
  // agents array stays in sync; otherwise a later updateTeam(..., team.agents)
  // from the grid would echo a stale (empty) array and wipe the saved config.
  refreshTeam: async (id) => {
    const t = await GetTeam(id);
    set((s) => ({
      teams: replaceTeam(s.teams, id, t),
    }));
  },

  deleteTeam: async (id) => {
    await DeleteTeam(id);
    set((s) => removeTeamState(s, id));
  },

  removeTeamLocal: (id) => set((s) => removeTeamState(s, id)),
}));
