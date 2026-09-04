// File-tree filter — the files section's quick-filter box state (zustand, so
// both trees, repos/ and home, follow the same query). The scope chooses which
// subtree receives recursive search while leaving normal browsing unchanged.
// Matching is a
// case-insensitive substring test over a row's displayed name (its filename, or
// the folded "a/b/c" path for single-child dir chains). It filters the rows the
// tree currently shows (loaded / expanded), the same as the repositories filter —
// see [[filter]], from which normQuery is reused.
//
// It also carries the focus bridge between the box and the tree, since they live
// in sibling components: Ctrl+F in the tree bumps focusInput; Enter in the box
// bumps focusTree. Each side watches its counter and moves focus (the repos tree
// is the one Enter returns to).
import { create } from "zustand";

interface FilesFilterStore {
  q: string;
  setQ(q: string): void;
  scope: "repos" | "home";
  setScope(scope: "repos" | "home"): void;
  /** Bumped to ask the filter box to take focus (Ctrl+F from the tree). */
  focusInputN: number;
  focusInput(): void;
  /** Bumped to ask the (repos) tree to take focus (Enter from the box). */
  focusTreeN: number;
  focusTree(): void;
}

export const useFilesFilter = create<FilesFilterStore>((set) => ({
  q: "",
  setQ: (q) => set({ q }),
  scope: "repos",
  setScope: (scope) => set({ scope }),
  focusInputN: 0,
  focusInput: () => set((s) => ({ focusInputN: s.focusInputN + 1 })),
  focusTreeN: 0,
  focusTree: () => set((s) => ({ focusTreeN: s.focusTreeN + 1 })),
}));
