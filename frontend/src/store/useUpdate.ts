import { create } from "zustand";
import { UpdateInfo } from "../lib/types";

// useUpdate holds the single in-app "a newer release is available" notification (#83).
// The banner shows while `info` is set and the user hasn't dismissed it. Dismissal is
// session-only (not persisted) — a still-outdated build reminds again on next launch.
interface UpdateState {
  info: UpdateInfo | null;
  dismissed: boolean;
  // setUpdate un-dismisses: a fresh check (manual re-check or a newly-published
  // release) should re-surface the banner even if a prior one was dismissed.
  setUpdate: (info: UpdateInfo) => void;
  dismiss: () => void;
}

export const useUpdate = create<UpdateState>((set) => ({
  info: null,
  dismissed: false,
  setUpdate: (info) => set({ info, dismissed: false }),
  dismiss: () => set({ dismissed: true }),
}));
