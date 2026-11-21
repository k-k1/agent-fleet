// Left-rail visibility (desktop dock/collapse + push/overlay mode). Lifted out of the
// App shell's local useState so the keyboard command "toggle left rail" (Ctrl+B) can
// drive it from outside React. Persists to localStorage exactly as the old App state
// did (keys af-left-open / af-left-mode). The mobile off-canvas drawer (navOpen) stays
// local to App — it is entangled with the drawer↔history popstate logic.
//
// The pane shows one view at a time (ADR 0140); `view` is which one. Every view stays
// mounted and only the chosen one is displayed, so switching never drops a draft, a scroll
// position or a poller that lives in a section.
import { create } from "zustand";

export const RAIL_VIEWS = ["sessions", "shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"] as const;
export type RailView = (typeof RAIL_VIEWS)[number];

const readView = (): RailView => {
  try {
    const v = localStorage.getItem("af-left-view");
    return (RAIL_VIEWS as readonly string[]).includes(v || "") ? (v as RailView) : "sessions";
  } catch {
    return "sessions";
  }
};

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
  view: RailView;
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
  /** Bring a view on screen: select it and dock the rail open. For anything that reveals a
   * row in the rail — it must never close a rail that is already showing the view. */
  showView(view: RailView): void;
  /** The activity bar's click: the icon of the view on screen folds the rail away, any other
   * icon (or any icon while the rail is folded) shows that view. */
  pickView(view: RailView): void;
}

export const useLeftRail = create<LeftRailStore>((set) => ({
  open: readOpen(),
  mode: readMode(),
  view: readView(),
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
  showView: (view) => {
    save("af-left-open", "1");
    save("af-left-view", view);
    set({ open: true, view });
  },
  pickView: (view) =>
    set((s) => {
      if (s.open && s.view === view) {
        save("af-left-open", "0");
        return { open: false, swipeOverlay: false };
      }
      save("af-left-open", "1");
      save("af-left-view", view);
      return { open: true, view };
    }),
}));
