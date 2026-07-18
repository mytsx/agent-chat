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
  setUpdate: (info) =>
    set((s) => ({
      info,
      // Only a genuinely NEWER version re-surfaces a dismissed banner. Re-setting the
      // SAME version preserves the dismissal — otherwise the push+pull channels both
      // delivering the same startup result (or a manual re-check finding the same
      // version) would un-dismiss a banner the user just closed. The SettingsModal
      // gives its own feedback for a manual same-version check, so the banner needn't
      // reappear.
      dismissed: s.info?.version === info.version ? s.dismissed : false,
    })),
  dismiss: () => set({ dismissed: true }),
}));
