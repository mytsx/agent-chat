import { create } from "zustand";
import { RoomSummary } from "../lib/types";
import { ListRooms } from "../../wailsjs/go/main/App";

interface RoomsState {
  rooms: RoomSummary[];
  loading: boolean;
  error: string | null;
  selectedRoom: string | null;

  loadRooms: () => Promise<void>;
  selectRoom: (name: string | null) => void;
}

export const useRooms = create<RoomsState>((set) => ({
  rooms: [],
  loading: false,
  error: null,
  selectedRoom: null,

  loadRooms: async () => {
    set({ loading: true, error: null });
    try {
      const rooms = await ListRooms();
      set({ rooms: (rooms as RoomSummary[]) || [], loading: false });
    } catch (e) {
      // ListRooms now rejects on hub errors (App.ListRooms returns an error);
      // surface it instead of silently showing an empty list.
      if (import.meta.env.DEV) console.warn("Failed to load rooms:", e);
      set({ error: e instanceof Error ? e.message : String(e), loading: false });
    }
  },

  selectRoom: (name) => set({ selectedRoom: name }),
}));
