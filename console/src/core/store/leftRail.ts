// Left-rail visibility (desktop dock/collapse + push/overlay mode). Lifted out of the
// App shell's local useState so the keyboard command "toggle left rail" (Ctrl+B) can
// drive it from outside React. Persists to localStorage exactly as the old App state
// did (keys af-left-open / af-left-mode). The mobile off-canvas drawer (navOpen) stays
// local to App — it is entangled with the drawer↔history popstate logic.
import { create } from "zustand";

const readOpen = (): boolean => {
  try {
    return localStorage.getItem("af-left-open") !== "0";
  } catch {
    return true;
  }
};
const readMode = (): "push" | "overlay" => {
  try {
    return localStorage.getItem("af-left-mode") === "overlay" ? "overlay" : "push";
  } catch {
    return "push";
  }
};
const save = (k: string, v: string): void => {
  try {
    localStorage.setItem(k, v);
  } catch {}
};

interface LeftRailStore {
  open: boolean;
  mode: "push" | "overlay";
  /** Transient: a tablet edge-swipe floated the rail as an overlay for that reveal,
   * regardless of `mode` (adds the .left-overlay class + dismiss backdrop). Cleared on
   * any close / manual toggle so the user's chosen mode takes back over. */
  swipeOverlay: boolean;
  toggle(): void;
  close(): void;
  /** Dock the rail open (no-op if already open). */
  ensureOpen(): void;
  toggleMode(): void;
  /** Tablet swipe-to-reveal (>760px, touch): open the rail floated as an overlay. */
  openOverlay(): void;
}

export const useLeftRail = create<LeftRailStore>((set) => ({
  open: readOpen(),
  mode: readMode(),
  swipeOverlay: false,
  toggle: () =>
    set((s) => {
      const open = !s.open;
      save("af-left-open", open ? "1" : "0");
      return { open, swipeOverlay: false };
    }),
  close: () => {
    save("af-left-open", "0");
    set({ open: false, swipeOverlay: false });
  },
  /** Ensure the rail is docked open (no-op if already open). Used when a command
   * reveals something in the rail — it must not toggle a visible rail shut. */
  ensureOpen: () => {
    save("af-left-open", "1");
    set({ open: true });
  },
  toggleMode: () =>
    set((s) => {
      const mode: "push" | "overlay" = s.mode === "push" ? "overlay" : "push";
      save("af-left-mode", mode);
      return { mode };
    }),
  openOverlay: () => {
    save("af-left-open", "1");
    set({ open: true, swipeOverlay: true });
  },
}));
