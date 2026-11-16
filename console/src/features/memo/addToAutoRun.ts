// The pure side of "Add to auto-run" on a work item: which repository the picker starts on, which
// categories it offers and in what order, and the note the Control Plane is asked to create. No
// React, so the choices are testable without the dialog.
import { repoBaseName } from "../../lib/workingSets.ts";
import type { Repo } from "../repos/store.ts";
import type { MemoCategory, MemoInput, MemoQueue, MemoReference } from "../../types/memo.ts";

/** The repository the picker starts on: the one a launch from this item would default to
 *  (`defaultRepo`, resolved by resolveLaunchRepo), as its PARENT repository when that is a
 *  worktree. Notes are filed per base repository, never under a worktree folder, so a worktree
 *  name here would put the note in a bucket nobody looks in. "" = nothing resolved: the member
 *  chooses, as in the launch panel. */
export function defaultMemoRepo(repos: readonly Repo[], defaultRepo: string): string {
  const hit = repos.find((r) => r.name === defaultRepo);
  if (!hit) return "";
  const base = repoBaseName(hit);
  return repos.some((r) => r.name === base && !r.worktree) ? base : "";
}

/** The repositories a note can be filed under: base clones only. */
export const memoRepoChoices = (repos: readonly Repo[]): string[] => repos.filter((r) => !r.worktree).map((r) => r.name);

export interface CategoryChoice {
  category: MemoCategory;
  /** The category has auto-run settings (on or off). */
  autoRun: boolean;
  /** Its auto-run is switched off: the note still waits in it, but nothing starts by itself. */
  off: boolean;
}

/** One repository's categories in the order the picker lists them: the auto-run ones first (in
 *  the order the memo list has them), the others below. In an ordinary category the note is an
 *  ordinary note. A category counts as auto-run when it has settings, as the memo list's badge
 *  does, whether or not they are switched on. */
export function categoryChoices(cats: readonly MemoCategory[], queues: readonly MemoQueue[], repo: string): CategoryChoice[] {
  const byCat = new Map(queues.map((q) => [q.categoryId, q]));
  const own = cats.filter((c) => c.repo === repo).sort((a, b) => a.position - b.position);
  const all = own.map((category): CategoryChoice => {
    const q = byCat.get(category.id);
    const autoRun = !!q || !!category.queue;
    return { category, autoRun, off: !!q && !q.enabled };
  });
  return [...all.filter((c) => c.autoRun), ...all.filter((c) => !c.autoRun)];
}

/** The request that files the item: the pointer, the member's own text when they typed one, and
 *  nothing of the item's body. */
export function addToAutoRunInput(repo: string, category: string, reference: MemoReference, note: string): MemoInput {
  return { kind: "text", repo, category, body: note.trim(), reference };
}
