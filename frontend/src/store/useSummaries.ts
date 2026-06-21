import { create } from "zustand";
import { RoomSummaryInfo } from "../lib/types";
import { GetRoomSummary, SaveRoomSummary } from "../../wailsjs/go/main/App";

// useSummaries caches each room's latest saved session summary (#29) so the
// modal, the continue panel, and any "özet var" indicator share one source of
// truth. Transcript + rendered-prompt reads stay out of the store (they are
// large and transient — the modal fetches them on demand).
interface SummariesState {
  summaries: Record<string, RoomSummaryInfo>;
  loadSummary: (room: string) => Promise<RoomSummaryInfo | null>;
  saveSummary: (room: string, text: string) => Promise<RoomSummaryInfo>;
}

export const useSummaries = create<SummariesState>((set) => ({
  summaries: {},

  loadSummary: async (room) => {
    try {
      const info = (await GetRoomSummary(room)) as RoomSummaryInfo;
      set((s) => ({ summaries: { ...s.summaries, [room]: info } }));
      return info;
    } catch (e) {
      if (import.meta.env.DEV) console.warn("loadSummary failed:", e);
      return null;
    }
  },

  saveSummary: async (room, text) => {
    const info = (await SaveRoomSummary(room, text)) as RoomSummaryInfo;
    set((s) => ({ summaries: { ...s.summaries, [room]: info } }));
    return info;
  },
}));

const EMPTY: RoomSummaryInfo | undefined = undefined;

// useSummaryFor returns the cached summary for a room (undefined until loaded).
export function useSummaryFor(room: string): RoomSummaryInfo | undefined {
  return useSummaries((s) => s.summaries[room] ?? EMPTY);
}
