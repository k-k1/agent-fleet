// Transient UI state for the keyboard system (zustand). Holds only what overlays
// render from — the dispatcher owns the logic and drives these setters. Kept separate
// from layout/settings stores per the store-per-domain principle (precedent:
// features/settings/store.ts useSettingsUI).
import { create } from "zustand";
import type { Region } from "../../lib/keys/registry.ts";

interface KeysStore {
  /** A leader chord was pressed; the next key(s) resolve a group/action. */
  leaderPending: boolean;
  /** Keys pressed after the leader so far, e.g. ["p"] while choosing a pane action. */
  leaderPath: string[];
  /** which-key overlay is visible (leaderPending + the short reveal delay elapsed). */
  whichKeyOpen: boolean;
  paletteOpen: boolean;
  /** The "?" shortcut cheat-sheet overlay is open. */
  cheatOpen: boolean;
  /** Which screen region has keyboard focus (drives F6 cycling + the focus ring). */
  activeRegion: Region;
  /** Bumped to ask the ACTIVE pane's PaneFind to open (it self-selects on `active`).
   * A signal rather than a handler registry, so multiple mounted PaneFinds don't
   * collide. */
  findSeq: number;

  /** Enter/advance leader mode with the given path, or clear it when null. */
  setLeader(path: string[] | null): void;
  setWhichKey(open: boolean): void;
  openPalette(): void;
  closePalette(): void;
  openCheat(): void;
  closeCheat(): void;
  setRegion(r: Region): void;
  requestFind(): void;
}

export const useKeysStore = create<KeysStore>((set) => ({
  leaderPending: false,
  leaderPath: [],
  whichKeyOpen: false,
  paletteOpen: false,
  cheatOpen: false,
  activeRegion: "main",
  findSeq: 0,

  setLeader: (path) =>
    set(
      path
        ? { leaderPending: true, leaderPath: path }
        : { leaderPending: false, leaderPath: [], whichKeyOpen: false },
    ),
  setWhichKey: (open) => set({ whichKeyOpen: open }),
  openPalette: () =>
    set({ paletteOpen: true, cheatOpen: false, leaderPending: false, leaderPath: [], whichKeyOpen: false }),
  closePalette: () => set({ paletteOpen: false }),
  openCheat: () =>
    set({ cheatOpen: true, paletteOpen: false, leaderPending: false, leaderPath: [], whichKeyOpen: false }),
  closeCheat: () => set({ cheatOpen: false }),
  setRegion: (r) => set({ activeRegion: r }),
  requestFind: () => set((s) => ({ findSeq: s.findSeq + 1 })),
}));
