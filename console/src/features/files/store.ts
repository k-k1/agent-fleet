// Files store (zustand): cross-section signals for the Files tree — a reveal
// request (expand + select a path: repo clicked / just cloned) and a refresh
// tick (workspace lifecycle / clone / upload elsewhere). Replaces the old
// filesKey bump counter + reveal state in the God-context.
import { create } from "zustand";

interface FilesStore {
  /** Reveal request: home-relative path + a counter so repeats re-trigger. */
  reveal: { path: string | null; n: number };
  revealInFiles(path: string): void;
  /** Refresh tick: the tree refetches root + open dirs when this bumps. */
  tick: number;
  bump(): void;
}

export const useFilesStore = create<FilesStore>((set) => ({
  reveal: { path: null, n: 0 },
  revealInFiles: (path) => set((s) => ({ reveal: { path, n: s.reveal.n + 1 } })),
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
}));
