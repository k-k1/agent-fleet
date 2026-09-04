// Files store (zustand): cross-section signals for the Files tree — a reveal
// request (expand + select a path: repo clicked / just cloned), a refresh
// tick (workspace lifecycle / clone / upload elsewhere) and a SCOPED refresh
// (one working copy, when a session's turn ends). Replaces the old filesKey
// bump counter + reveal state in the God-context.
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
  /**
   * Scoped auto-refresh: re-read only what is on screen UNDER `prefix`
   * (home-relative, e.g. "repos/agent-fleet@wip-a"). Bumped when a session's
   * turn ends (sessionRefresh.ts) — the whole point is that it costs less than
   * `bump()`, which re-reads the root and every open dir of every tree.
   *
   * `n` is what consumers watch; `prefix` alone would not re-trigger for two
   * turns ending in the same working copy.
   */
  scoped: { prefix: string; n: number };
  refreshUnder(prefix: string): void;
}

export const useFilesStore = create<FilesStore>((set) => ({
  reveal: { path: null, n: 0, focus: false },
  revealInFiles: (path, opts) => set((s) => ({ reveal: { path, n: s.reveal.n + 1, focus: !!opts?.focus } })),
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
  scoped: { prefix: "", n: 0 },
  refreshUnder: (prefix) => set((s) => ({ scoped: { prefix, n: s.scoped.n + 1 } })),
}));
