// Left-rail visibility (desktop dock/collapse + push/overlay mode). Lifted out of the
// App shell's local useState so the keyboard command "toggle left rail" (Ctrl+B) can
// drive it from outside React. Persists to localStorage exactly as the old App state
// did (keys af-left-open / af-left-mode). The mobile off-canvas drawer (navOpen) stays
// local to App — it is entangled with the drawer↔history popstate logic and keyboard
// nav is desktop-only.
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
  toggle(): void;
  close(): void;
  toggleMode(): void;
}

export const useLeftRail = create<LeftRailStore>((set) => ({
  open: readOpen(),
  mode: readMode(),
  toggle: () =>
    set((s) => {
      const open = !s.open;
      save("af-left-open", open ? "1" : "0");
      return { open };
    }),
  close: () => {
    save("af-left-open", "0");
    set({ open: false });
  },
  toggleMode: () =>
    set((s) => {
      const mode: "push" | "overlay" = s.mode === "push" ? "overlay" : "push";
      save("af-left-mode", mode);
      return { mode };
    }),
}));
