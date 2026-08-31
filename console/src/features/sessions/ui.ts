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
  cleanupOpen: boolean; // the cleanup panel (docs/log/32)
  openRename(s: Session): void;
  openBranchRename(s: Session): void;
  openSsmResume(name: string, force: boolean): void;
  openArchived(): void;
  openCleanup(): void;
  close(): void; // clears every session dialog
}

export const useSessionUI = create<SessionUI>((set) => ({
  rename: null,
  branchRename: null,
  ssmResume: null,
  archivedOpen: false,
  cleanupOpen: false,
  openRename: (s) => set({ rename: s }),
  openBranchRename: (s) => set({ branchRename: s }),
  openSsmResume: (name, force) => set({ ssmResume: { name, force } }),
  openArchived: () => set({ archivedOpen: true }),
  openCleanup: () => set({ cleanupOpen: true }),
  close: () =>
    set({ rename: null, branchRename: null, ssmResume: null, archivedOpen: false, cleanupOpen: false }),
}));
