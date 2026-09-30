// The Console's side of the two-stage stop (ADR 0105): which queue the chat shows, when the
// stop control is drawn, and what a discard notice offers back. Pure, so the branches that
// decide what a member can reach are tested without mounting the mirror.
import { isMemberOrigin } from "../../core/api/client.ts";
import type { Discard, QueueItem } from "../../core/api/client.ts";

/** One queued input as the chat draws it. `item` is present only when the Agent sent
 *  `queuedItems` (a Managed session on an ADR 0105 Agent); without it the bubble has no
 *  actions, because there is no id to act on. */
export interface QueueEntry {
  text: string;
  item?: QueueItem;
}

/** queueEntries picks the richer of the two queue views. `queuedItems` wins whenever the Agent
 *  sent it; `queuedPrompts` stays the source for the claude TUI route and an older Agent. */
export function queueEntries(items: QueueItem[] | null, prompts: string[]): QueueEntry[] {
  if (items) return items.map((item) => ({ text: item.text, item }));
  return prompts.map((text) => ({ text }));
}

/** actionable says whether a bubble may offer "back to the input box" and "remove": only an
 *  entry that is still cancellable (decision 5). Committed and sent entries are shown bare. */
export const actionable = (item: QueueItem | undefined): item is QueueItem => !!item && item.state === "queued";

/** restorable says whether a bubble may also offer "back to the input box": a still-queued
 *  entry the member wrote (decision 4). Removing is open to every origin. */
export const restorable = (item: QueueItem | undefined): boolean => actionable(item) && isMemberOrigin(item.origin);

/** injectionSource maps a queued entry's origin onto Turn.source, so a queued peer or
 *  schedule input wears the same badge it will wear once it runs. The member's own input
 *  carries none. */
export function injectionSource(item: QueueItem | undefined): { source?: string; peerFrom?: string } {
  const kind = item?.origin?.kind;
  if (!kind || kind === "member") return {};
  if (kind === "peer" && item?.origin?.from) return { source: kind, peerFrom: item.origin.from };
  return { source: kind };
}

export interface StopRowInput {
  managed: boolean;
  /** A turn runs, a background run lingers, or the reply is still landing. */
  busy: boolean;
  /** The chat shows something queued (possibly stale: the server may hold more). */
  queued: boolean;
  /** A question card is up. */
  question: boolean;
  /** A Managed tool-approval card is up. */
  approval: boolean;
}

/** stopRowVisible decides whether the stop control is drawn.
 *
 *  Terminal (CLI) keeps its old rule: while busy, and not under a question, whose card owns
 *  the Esc (its Cancel). A Managed session must always be able to reach the brake while it
 *  runs or holds anything (decision 3), and that includes the time a question or approval
 *  is shown: a codex question can be raised with input already queued behind it. */
export function stopRowVisible(s: StopRowInput): boolean {
  if (!s.managed) return s.busy && !s.question;
  return s.busy || s.queued || s.question || s.approval;
}

/** What a discard notice shows: the member's own input, which it can put back into the
 *  input box, and everything else, which it only lists by origin (decision 4). */
export interface DiscardView {
  discard: Discard;
  member: QueueItem[];
  others: QueueItem[];
}

export function discardView(d: Discard): DiscardView {
  const member: QueueItem[] = [];
  const others: QueueItem[] = [];
  for (const it of d.items) (isMemberOrigin(it.origin) ? member : others).push(it);
  return { discard: d, member, others };
}

/** Where a tab stands with one discard: how many of its member entries it has put back. */
export interface DiscardProgress {
  restored: number;
}

export interface DiscardNoticeState {
  /** Discards this tab is part-way through restoring, with the discard itself: the count is
   *  this tab's alone, and a poll that drops the discard (another tab finished or closed it)
   *  must not take the rest away from under a member who is still putting them back. */
  held: Record<string, { discard: Discard; progress: DiscardProgress }>;
  /** Discards this tab is done with. A poll that still carries one (it left before the
   *  dismissal landed) must not bring the notice back. */
  closed: Record<string, true>;
}

export const emptyDiscardNotices: DiscardNoticeState = { held: {}, closed: {} };

/** visibleDiscards merges the poll with what this tab holds, oldest first. */
export function visibleDiscards(polled: Discard[], st: DiscardNoticeState): { view: DiscardView; restored: number }[] {
  const out = new Map<string, { view: DiscardView; restored: number }>();
  for (const d of polled) if (!st.closed[d.id]) out.set(d.id, { view: discardView(d), restored: 0 });
  for (const [id, h] of Object.entries(st.held)) {
    if (!st.closed[id]) out.set(id, { view: discardView(h.discard), restored: h.progress.restored });
  }
  return [...out.values()].sort((a, b) => a.view.discard.at.localeCompare(b.view.discard.at));
}

/** restoreStep takes the next member entry of a discard. It returns the entry, the new state,
 *  and whether this step must tell the driver to drop the discard.
 *
 *  Only the step that puts back the LAST member entry dismisses it, and that step also closes
 *  the notice (what is left is the list of other origins, which the member has seen). A
 *  restore is the whole one-by-one run (decision 4): dismissing on the first entry would lose
 *  the rest on the server too if this tab closed half-way. */
export function restoreStep(
  st: DiscardNoticeState,
  d: Discard,
): { item: QueueItem | null; next: DiscardNoticeState; dismiss: boolean } {
  if (st.closed[d.id]) return { item: null, next: st, dismiss: false };
  const view = discardView(d);
  const restored = st.held[d.id]?.progress.restored ?? 0;
  const item = view.member[restored] ?? null;
  if (!item) return { item: null, next: st, dismiss: false };
  const done = restored + 1 >= view.member.length;
  const held = { ...st.held };
  if (done) delete held[d.id];
  else held[d.id] = { discard: d, progress: { restored: restored + 1 } };
  return { item, next: { held, closed: done ? { ...st.closed, [d.id]: true } : st.closed }, dismiss: done };
}

/** closeStep closes a notice, which always tells the driver: a discard with no member entry
 *  (peer, schedule … only) is dismissed here and nowhere else. */
export function closeStep(st: DiscardNoticeState, id: string): { next: DiscardNoticeState; dismiss: boolean } {
  const held = { ...st.held };
  delete held[id];
  return { next: { held, closed: { ...st.closed, [id]: true } }, dismiss: true };
}
