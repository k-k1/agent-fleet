// What the approval cards have shown of each held note (#4209), kept in module memory: it survives
// the section being drawn again (a layout switch renders it in another place, a note leaves the
// card and comes back) and is gone on a page reload, which draws every card afresh. Records are
// keyed by tenant and note id, and live until the person approves the note, a run consumes it, or it is deleted.
import { confirmShown, forgetShown, shownStatus, trackShown, type ShownNote } from "./queues.ts";
import type { Memo } from "../../types/memo.ts";

let records: ReadonlyMap<string, ShownNote> = new Map();

const keyer = (tenant: string) => (id: string) => tenant + "\x00" + id;

/** Folds the live list in and answers which notes changed and which still wait for a confirmation.
 *  Idempotent for one list, so a repeated render marks nothing new. */
export function trackNotes(tenant: string, memos: readonly Memo[]): { edited: Set<string>; awaiting: Set<string> } {
  const keyOf = keyer(tenant);
  records = trackShown(records, memos, keyOf, (k) => k.startsWith(tenant + "\x00"));
  return shownStatus(records, memos, keyOf);
}

export function confirmNotes(tenant: string, items: readonly Memo[]): void {
  records = confirmShown(records, items, keyer(tenant));
}

export function forgetNotes(tenant: string, items: readonly Memo[]): void {
  records = forgetShown(records, items, keyer(tenant));
}

/** A page reload in a test: nothing is kept. */
export function resetShownNotes(): void {
  records = new Map();
}
