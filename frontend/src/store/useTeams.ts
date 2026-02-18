import { create } from "zustand";
import { Team, AgentConfig } from "../lib/types";
import {
  ListTeams,
  CreateTeam,
  UpdateTeam,
  DeleteTeam,
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
  deleteTeam: (id: string) => Promise<void>;
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
}));
