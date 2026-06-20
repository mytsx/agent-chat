import { create } from "zustand";
import { RoomSummary } from "../lib/types";
import { ListRooms } from "../../wailsjs/go/main/App";

interface RoomsState {
  rooms: RoomSummary[];
  loading: boolean;
  selectedRoom: string | null;

  loadRooms: () => Promise<void>;
  selectRoom: (name: string | null) => void;
}

export const useRooms = create<RoomsState>((set) => ({
  rooms: [],
  loading: false,
  selectedRoom: null,

  loadRooms: async () => {
    set({ loading: true });
    try {
      const rooms = await ListRooms();
      set({ rooms: (rooms as RoomSummary[]) || [], loading: false });
    } catch (e) {
      if (import.meta.env.DEV) console.warn("Failed to load rooms:", e);
      set({ loading: false });
    }
  },

  selectRoom: (name) => set({ selectedRoom: name }),
}));
