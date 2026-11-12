// Session UI store (zustand): the per-row modal triggers, lifted out of any one
// section so a SessionRow works the same wherever it renders — the flat list, the
// project tree's working-copy nodes, the orphan catch-all. A single app-level
// <SessionModals> host reads this and renders the dialogs; rows just fire the
// openers. (The "new session" signal stays on the sessions store as newSessionTick.)
import { create } from "zustand";
import type { Session } from "../../types/session.ts";

interface SessionUI {
  rename: Session | null; // title-edit modal target
  branchRename: Session | null; // worktree branch-rename modal target
  ssmResume: { name: string; force: boolean } | null; // SSM re-login/resume target
  archivedOpen: boolean; // the archive browser
  archivedDir: string | null; // …scoped to one working copy's folder, when opened from its row
  cleanupOpen: boolean; // the cleanup panel (docs/log/32)
  reclaimOpen: boolean; // the confirm behind the low-disk notice's "Clean regenerable items…" (#3880)
  budget: Session | null; // spend budget / "raise and resume" target (#1054)
  moveFromLimit: boolean; // …opened from the mirror's limit banner (quota-left kinds first)
  move: Session | null; // move to another agent kind, and its retry / cancel / reopen (ADR 0115)
  openRename(s: Session): void;
  openBranchRename(s: Session): void;
  openSsmResume(name: string, force: boolean): void;
  openArchived(dir?: string): void;
  openCleanup(): void;
  openReclaim(): void;
  openBudget(s: Session): void;
  openMove(s: Session, fromLimit?: boolean): void;
  close(): void; // clears every session dialog
}

export const useSessionUI = create<SessionUI>((set) => ({
  rename: null,
  branchRename: null,
  ssmResume: null,
  archivedOpen: false,
  archivedDir: null,
  cleanupOpen: false,
  reclaimOpen: false,
  budget: null,
  move: null,
  moveFromLimit: false,
  openRename: (s) => set({ rename: s }),
  openBranchRename: (s) => set({ branchRename: s }),
  openSsmResume: (name, force) => set({ ssmResume: { name, force } }),
  openArchived: (dir) => set({ archivedOpen: true, archivedDir: dir || null }),
  openCleanup: () => set({ cleanupOpen: true }),
  openReclaim: () => set({ reclaimOpen: true }),
  openBudget: (s) => set({ budget: s }),
  openMove: (s, fromLimit = false) => set({ move: s, moveFromLimit: fromLimit }),
  close: () =>
    set({ rename: null, branchRename: null, ssmResume: null, archivedOpen: false, archivedDir: null, cleanupOpen: false, reclaimOpen: false, budget: null, move: null, moveFromLimit: false }),
}));
