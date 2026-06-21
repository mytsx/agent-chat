import { create } from "zustand";
import { Team, AgentConfig } from "../lib/types";
import {
  ListTeams,
  GetTeam,
  CreateTeam,
  UpdateTeam,
  DeleteTeam,
  SetTeamManager,
  SetCustomPrompt,
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
  // setCustomPrompt updates a room's charter (start-of-room context injected into
  // each new agent's startup prompt). Uses the dedicated backend endpoint rather
  // than updateTeam, which omits custom_prompt and would reset it on layout change.
  setCustomPrompt: (id: string, text: string) => Promise<void>;
  refreshTeam: (id: string) => Promise<void>;
  deleteTeam: (id: string) => Promise<void>;
  // removeTeamLocal drops a team from client state only (no backend call). Used
  // for create-rollback cleanup: deleteTeam filters state only after its backend
  // binding resolves, so if that delete also fails the optimistic tab would linger.
  removeTeamLocal: (id: string) => void;
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
      teams: s.teams.map((team) => (team.id === id ? t : team)),
    }));
  },

  setTeamManager: async (id, managerAgent) => {
    const t = await SetTeamManager(id, managerAgent);
    set((s) => ({
      teams: s.teams.map((team) => (team.id === id ? t : team)),
    }));
  },

  setCustomPrompt: async (id, text) => {
    const t = await SetCustomPrompt(id, text);
    set((s) => ({
      teams: s.teams.map((team) => (team.id === id ? t : team)),
    }));
  },

  // Re-pull a team from the backend. Needed after the backend persists agents
  // (CreateTerminal/OpenTeamFromConfig → UpsertAgent) so the in-memory team's
  // agents array stays in sync; otherwise a later updateTeam(..., team.agents)
  // from the grid would echo a stale (empty) array and wipe the saved config.
  refreshTeam: async (id) => {
    const t = await GetTeam(id);
    set((s) => ({
      teams: s.teams.map((team) => (team.id === id ? t : team)),
    }));
  },

  deleteTeam: async (id) => {
    await DeleteTeam(id);
    set((s) => {
      const teams = s.teams.filter((t) => t.id !== id);
      return {
        teams,
        activeTeamID:
          s.activeTeamID === id ? (teams[0]?.id ?? null) : s.activeTeamID,
      };
    });
  },

  removeTeamLocal: (id) =>
    set((s) => {
      const teams = s.teams.filter((t) => t.id !== id);
      return {
        teams,
        activeTeamID:
          s.activeTeamID === id ? (teams[0]?.id ?? null) : s.activeTeamID,
      };
    }),
}));
