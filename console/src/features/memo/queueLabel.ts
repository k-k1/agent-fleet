import type { MemoQueue } from "../../types/memo.ts";

/** A queue's heading, `repo / category`, shared by the Auto-run view and the Active view so
 *  one queue reads the same in both. Empty when the queue names neither; the caller picks the
 *  fallback. */
export function queueLabel(q: Pick<MemoQueue, "repo" | "category">): string {
  return [q.repo, q.category].filter(Boolean).join(" / ");
}
