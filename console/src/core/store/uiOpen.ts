// uiOpen — a keyboard→popover bridge for the always-mounted status surfaces that own their
// own open state (the notification center + the WsBar's Claude / Codex usage and resource
// chips). Each is an anchored popover with local `open` state + useDismiss (outside-click /
// Esc), so we can't drive them from a shared boolean without stealing that ownership. Instead
// a keyboard command bumps a per-target counter here and the owning component toggles itself
// when its counter changes (same signal pattern as keysStore.findSeq → PaneFind).
import { useEffect, useRef } from "react";
import { create } from "zustand";

export type OpenTarget = "notifications" | "usage-claude" | "usage-codex" | "usage-agy" | "resources";

interface UiOpenStore {
  /** Per-target monotonic request counter. A bump = "the user asked to toggle this". */
  seq: Record<OpenTarget, number>;
  toggle(t: OpenTarget): void;
}

export const useUiOpen = create<UiOpenStore>((set) => ({
  seq: { notifications: 0, "usage-claude": 0, "usage-codex": 0, "usage-agy": 0, resources: 0 },
  toggle: (t) => set((s) => ({ seq: { ...s.seq, [t]: s.seq[t] + 1 } })),
}));

// Run `onToggle` whenever a keyboard command targets this surface. Skips the initial mount
// (the counter only matters on change) and reads the callback through a ref so an inline
// closure is safe — mirrors useDismiss's cb-ref discipline.
export function useOpenSignal(target: OpenTarget, onToggle: () => void): void {
  const cb = useRef(onToggle);
  cb.current = onToggle;
  const sig = useUiOpen((s) => s.seq[target]);
  const prev = useRef(sig);
  useEffect(() => {
    if (sig === prev.current) return;
    prev.current = sig;
    cb.current();
  }, [sig]);
}
