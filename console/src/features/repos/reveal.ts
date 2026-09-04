// revealRepoInRail — put a working copy on screen in the project tree and focus its
// row. Used by the command palette's repo rows (Enter = focus the left rail). Docks the
// rail open, then fires the reveal signal the tree's RepoNodes listen for (expand the
// node — and its base, for a worktree — then scroll + focus). Kept out of the palette so
// the store wiring lives with the repos feature.
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useRepoReveal } from "./store.ts";

export function revealRepoInRail(name: string): void {
  useLeftRail.getState().ensureOpen();
  useRepoReveal.getState().reveal(name);
}
