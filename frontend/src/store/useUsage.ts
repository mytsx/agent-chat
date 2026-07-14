import { create } from "zustand";
import { UsageUpdatedEvent } from "../lib/types";

// useUsage caches each session's latest usage:updated snapshot (#10) plus a
// reactive limitHits marker (bumped on usage:limit-hit) so PTY-driven limit
// signals can drive UI without a full snapshot round-trip.
interface UsageState {
  entries: Record<string, UsageUpdatedEvent>;
  limitHits: Record<string, number>; // sessionID → epoch ms (reaktif PTY sinyali)
  applySnapshot: (ev: UsageUpdatedEvent) => void;
  markLimitHit: (sessionID: string) => void;
  clearLimitHit: (sessionID: string) => void;
  clear: (sessionID: string) => void;
}

export const useUsage = create<UsageState>((set) => ({
  entries: {},
  limitHits: {},
  applySnapshot: (ev) =>
    set((s) => ({ entries: { ...s.entries, [ev.snapshot.sessionID]: ev } })),
  markLimitHit: (sessionID) =>
    set((s) => ({ limitHits: { ...s.limitHits, [sessionID]: Date.now() } })),
  // clearLimitHit removes ONLY the limit-hit marker (keeping the valid snapshot),
  // so dismissing the switch dialog stops the 🔄 pulse without dropping usage (#10).
  clearLimitHit: (sessionID) =>
    set((s) => {
      const limitHits = { ...s.limitHits };
      delete limitHits[sessionID];
      return { limitHits };
    }),
  clear: (sessionID) =>
    set((s) => {
      const entries = { ...s.entries };
      const limitHits = { ...s.limitHits };
      delete entries[sessionID];
      delete limitHits[sessionID];
      return { entries, limitHits };
    }),
}));

const EMPTY: UsageUpdatedEvent | undefined = undefined;

// useUsageFor returns the cached usage snapshot for a session (undefined until loaded).
export function useUsageFor(sessionID: string): UsageUpdatedEvent | undefined {
  return useUsage((s) => s.entries[sessionID] ?? EMPTY);
}

export function useAllUsage(): Record<string, UsageUpdatedEvent> {
  return useUsage((s) => s.entries);
}
