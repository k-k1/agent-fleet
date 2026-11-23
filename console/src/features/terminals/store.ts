// A request to show one place in the Terminal view (ADR 0142 decision 10): the count chip of a
// repository or space row in the Sessions view asks for the group of its shells. The view reads
// `key` when it mounts as well as on every bump, because the view is mounted the first time it
// is shown and the request that shows it arrives before that mount.
import { create } from "zustand";

interface TerminalRevealStore {
  /** A group key (project.ts shellPlaceKey / ssmMachineKey). */
  key: string | null;
  n: number;
  reveal(key: string): void;
}

export const useTerminalReveal = create<TerminalRevealStore>((set) => ({
  key: null,
  n: 0,
  reveal: (key) => set((s) => ({ key, n: s.n + 1 })),
}));
