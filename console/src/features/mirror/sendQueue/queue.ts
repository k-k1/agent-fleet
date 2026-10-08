// queue — the pure rules of the composer's pre-send queue (#1083).
//
// The queue holds follow-ups the member wrote while a turn was running and has not sent yet. It
// is per session, lives in client memory only, and is NOT the Memo queue (persistent, cross-device,
// docs/member/07-chat-memo.md) nor the queue the agent keeps for input it already accepted
// (ADR 0105). An item enters the transcript only when it is sent; until then it is a draft.

/** One held follow-up: the text as typed and the uploaded attachment paths that go with it. */
export interface QueuedSend {
  id: string;
  text: string;
  paths: string[];
}

export function enqueue(items: QueuedSend[], item: QueuedSend): QueuedSend[] {
  return [...items, item];
}

export function removeItem(items: QueuedSend[], id: string): QueuedSend[] {
  return items.some((i) => i.id === id) ? items.filter((i) => i.id !== id) : items;
}

/** Put an item back at `index` (clamped) — a failed send returns where it came from. */
export function insertAt(items: QueuedSend[], item: QueuedSend, index: number): QueuedSend[] {
  const at = Math.max(0, Math.min(index, items.length));
  return [...items.slice(0, at), item, ...items.slice(at)];
}

/** Replace an item's text. An edit that would leave the item empty is refused (returns the same list). */
export function editItem(items: QueuedSend[], id: string, text: string): QueuedSend[] {
  const t = text.trim();
  const cur = items.find((i) => i.id === id);
  if (!cur || (!t && !cur.paths.length) || cur.text === t) return items;
  return items.map((i) => (i.id === id ? { ...i, text: t } : i));
}

/** Move an item one place up (-1) or down (+1); at either end it stays. */
export function moveItem(items: QueuedSend[], id: string, delta: -1 | 1): QueuedSend[] {
  const from = items.findIndex((i) => i.id === id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= items.length) return items;
  const next = items.slice();
  [next[from], next[to]] = [next[to], next[from]];
  return next;
}

/**
 * How long after a drain send we wait to SEE the session go busy before releasing the next
 * item. The polled status can read idle for a moment after a send was accepted; without this
 * the next item would go out as a second "start" on top of the turn just begun. A turn that
 * really finished before we ever saw it busy (a slash command) is released by the timeout.
 */
export const DRAIN_SETTLE_MS = 6000;

export interface DrainState {
  count: number;
  /** The session is mid-exchange (working, background run, finalizing). */
  busy: boolean;
  /** Alive, writable, composer not locked by a pending decision, no send in flight. */
  canSend: boolean;
  /** The member stopped the turn: nothing goes out until they resume the queue. */
  paused: boolean;
  /** A queue send is awaiting its answer. */
  inflight: boolean;
  /** When the last drain send left, until busy has been seen once since. */
  awaitingSince: number | null;
  now: number;
}

/** True when exactly one head item may leave now. Everything else about "exactly once" is the caller removing it first. */
export function shouldDrain(s: DrainState): boolean {
  if (s.count === 0 || s.busy || !s.canSend || s.paused || s.inflight) return false;
  return s.awaitingSince === null || s.now - s.awaitingSince >= DRAIN_SETTLE_MS;
}

/**
 * Whether a send during a running turn is taken INTO that turn. Only codex and muse do (Managed:
 * a native turn/steer); every other kind queues the input behind the turn, so the "send now"
 * hint must not promise more (docs/member/07-chat-memo.md, "Stopping a turn when something is queued").
 */
export function injectsMidTurn(agentId: string, managed: boolean): boolean {
  return managed && (agentId === "codex" || agentId === "muse");
}
