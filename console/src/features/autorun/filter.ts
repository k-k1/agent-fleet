// The Auto-run view's quick filter: the same case-insensitive substring match as the Repositories
// view's (project/filter.ts), applied to a run. A run matches on what identifies it, shown or not:
// its category label, the work item (title, key, `#N` reference), its own session fields and those
// of the sessions it started, so a reviewer's name finds the run it belongs to.
import { normQuery, sessionMatches } from "../project/filter.ts";
import { matchSessionRef, refQuery, type RefIndex } from "../sessions/refSearch.ts";
import type { RunRow } from "./runs.ts";

export { normQuery };

/** `nq` is a normalised query (normQuery); "" matches everything. */
export function runMatches(run: RunRow, categoryLabel: string, nq: string, refs: RefIndex): boolean {
  if (!nq) return true;
  const hay = [categoryLabel, run.item?.key ?? "", run.item?.label ?? "", run.item?.title ?? ""].join("\n").toLowerCase();
  if (hay.includes(nq)) return true;
  // The run's own PR is a reference too (sessionMatches reads it from Session.pr).
  if (matchSessionRef(run.root, refs, refQuery(nq))) return true;
  return [run.root, ...run.children].some((s) => sessionMatches(s, nq, refs));
}
