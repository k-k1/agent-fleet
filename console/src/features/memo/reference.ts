// What a note's reference is, on the screen side: reading it off a note, finding the cached work
// item it points at, and the provider's mark. A reference is a pointer to an item and never a copy
// of it (ADR 0137 §4), so the only item text shown here is the title the work-item cache already
// holds, read at draw time and never stored with the note.
import type { Memo, MemoReference } from "../../types/memo.ts";
import type { WorkItem } from "../workitems/read.ts";

/** The reference of a note, or null when it has none. Anything the note holds is returned, even a
 *  half-filled one (an older or newer Control Plane): the approval card must never hide what the
 *  release is bound to, so whether it can be a link is decided where it is drawn (`linkableUrl`). */
export function memoReference(m: Pick<Memo, "reference">): MemoReference | null {
  const r = m.reference;
  if (!r || typeof r !== "object") return null;
  const f = (v: unknown) => (typeof v === "string" ? v : "");
  const out = { provider: f(r.provider), key: f(r.key), url: f(r.url) };
  return out.provider || out.key || out.url ? out : null;
}

/** An address that is safe to put in a link: http or https only, never `javascript:` and the like. */
export const linkableUrl = (url: string): boolean => /^https?:\/\/\S+$/i.test(url);

/** Keys of github and bitbucket compare without regard to case: the Control Plane stores them in
 *  lower case while the work-item cache keeps the provider's own spelling. Jira keys are
 *  upper-cased on both sides, so the same fold is harmless there. */
const foldKey = (provider: string, key: string): string =>
  provider === "github" || provider === "bitbucket" ? key.toLowerCase() : key;

/** The cached work item a reference points at, if the cache has it. */
export function findWorkItem(ref: MemoReference, items: readonly WorkItem[]): WorkItem | undefined {
  const want = foldKey(ref.provider, ref.key);
  return items.find((i) => i.provider === ref.provider && foldKey(i.provider, i.key) === want);
}

/** The provider's mark, the one the work-item section draws for a row: a pull request has its own
 *  icon, every other item the issue one. A reference carries no kind, so without a cached item it
 *  reads as an issue. */
export function referenceIcon(item: Pick<WorkItem, "kind"> | undefined): string {
  return item?.kind === "pr" ? "git-pull-request" : "issues";
}
