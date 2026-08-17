// Files store (zustand): cross-section signals for the Files tree — a reveal
// request (expand + select a path: repo clicked / just cloned) and a refresh
// tick (workspace lifecycle / clone / upload elsewhere). Replaces the old
// filesKey bump counter + reveal state in the God-context.
import { create } from "zustand";

interface FilesStore {
  /**
   * Reveal request: home-relative path + a counter so repeats re-trigger.
   * `focus` moves keyboard focus to the revealed row's tree, which is right when
   * the reader ASKED to go there (clicking a folder in a reply) and wrong when
   * the reveal is a side effect of something else finishing (a clone landing),
   * where it would yank focus out from under whatever they are typing.
   */
  reveal: { path: string | null; n: number; focus: boolean };
  revealInFiles(path: string, opts?: { focus?: boolean }): void;
  /** Refresh tick: the tree refetches root + open dirs when this bumps. */
  tick: number;
  bump(): void;
}

export const useFilesStore = create<FilesStore>((set) => ({
  reveal: { path: null, n: 0, focus: false },
  revealInFiles: (path, opts) => set((s) => ({ reveal: { path, n: s.reveal.n + 1, focus: !!opts?.focus } })),
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
}));
