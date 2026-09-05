// Comments on a plan (the equivalent of VSCode's plan review) - the primary store on the
// collecting side.
//
// The experience is split in two:
//   - collect: select text in the doc pane showing the plan (DocView) and add a comment.
//   - send:    review the list on the mirror's plan card and send it in one action.
// Because of that split it is not limited to "while approval is pending": even after a rejection,
// more comments can be sent to a session that went back to waiting for input while still in plan
// mode (the mirror picks the delivery route from the state).
//
// The grouping key (planKey) is "session name + hash of the plan text". A plan awaiting approval
// has no tool_use id (the payload is only the text), and the Console does not carry the history
// card's id around either, so the text itself is the only stable identity. A revised plan gets a
// different key, and the old comments stay on the card they were written against, so stale
// remarks cannot leak into the revised text.
//
// The anchor is "quoted string + which occurrence it is". Only the rendered DOM of the plan
// Markdown is available, so the position is restored by the nth match in the rendered text rather
// than by an offset (the equivalent of W3C Web Annotation's TextQuoteSelector). If the text
// changes the match is simply lost; a highlight is never attached to the wrong place.
//
// Storage is a single localStorage entry. With no server behind it, the `storage` event is
// subscribed to so that comments added in a detached tab (a pane shown in another tab) reach the
// mirror in the original tab. The key carries the `af` prefix, so clearLocalState cleans it up at
// logout.
import { useSyncExternalStore } from "react";
import { t } from "../../lib/i18n/index.ts";

export interface PlanComment {
  id: string;
  /** The selected excerpt of the plan text (as rendered). */
  quote: string;
  /** Which occurrence of quote this is in the text (0-based); prevents mixing up repeated words. */
  nth: number;
  /** The remark the user wrote. */
  body: string;
  ts: number;
  /** When it became sent (kept, folded rather than deleted - what was sent becomes the history). */
  sentAt?: number;
}

type Store = Record<string, PlanComment[]>;

const LS_KEY = "af.plan-comments";
/** Maximum number of stored groups (oldest key evicted first), so localStorage cannot grow without bound. */
const MAX_KEYS = 30;
/** Maximum number of comments on one plan. */
export const MAX_COMMENTS = 50;
/** Stored quote length. An over-long selection keeps only its head - enough as an anchor, and it does not wreck the layout. */
export const MAX_QUOTE = 300;

let store: Store = load();
const listeners = new Set<() => void>();
// useSyncExternalStore decides on re-render by the reference identity of getSnapshot, so keep one
// object that is swapped on every change (never build a fresh array per call).
let snapshot = store;

function load(): Store {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Store = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (!Array.isArray(v)) continue;
      const list = v.filter(isComment);
      if (list.length) out[k] = list;
    }
    return out;
  } catch {
    return {}; // private mode / corrupt JSON - comments are auxiliary, so start empty in silence
  }
}

function isComment(c: unknown): c is PlanComment {
  const o = c as PlanComment;
  return !!o && typeof o.id === "string" && typeof o.quote === "string" && typeof o.body === "string";
}

function persist() {
  snapshot = store;
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(store));
  } catch {
    /* quota / private mode: the in-memory state is still valid, so carry on */
  }
  for (const l of listeners) l();
}

function commit(next: Store) {
  // Over the limit, evict the groups whose most recently touched comment is the oldest.
  const keys = Object.keys(next);
  if (keys.length > MAX_KEYS) {
    const lastTouch = (k: string) => Math.max(0, ...next[k].map((c) => c.ts));
    for (const k of keys.sort((a, b) => lastTouch(a) - lastTouch(b)).slice(0, keys.length - MAX_KEYS)) {
      delete next[k];
    }
  }
  store = next;
  persist();
}

// Pick up additions made in another tab (a detached pane). The storage event is by specification
// not delivered to the originating tab, so this tab's own updates are covered by commit()'s
// notification instead - the two never run twice for one change.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== null && e.key !== LS_KEY) return; // null = clear() (logout)
    store = load();
    snapshot = store;
    for (const l of listeners) l();
  });
}

/** planKey groups by "which session, and which plan text". */
export function planKey(session: string, plan: string): string {
  return session + ":" + hash(normalizePlan(plan));
}

// Normalise only surrounding and trailing whitespace. A difference in content (a revision) must
// produce a different key, so do not normalise any further than that.
function normalizePlan(plan: string): string {
  return (plan || "")
    .split("\n")
    .map((l) => l.replace(/\s+$/, ""))
    .join("\n")
    .trim();
}

// FNV-1a (32-bit) - this use needs stability and brevity more than collision resistance (keys are
// already separated by session name).
function hash(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(36);
}

export function getPlanComments(key: string): PlanComment[] {
  return store[key] || EMPTY;
}
const EMPTY: PlanComment[] = [];

/** Unsent comments only (drives the send button's enabled state and builds the body). */
export const unsentComments = (list: PlanComment[]): PlanComment[] => list.filter((c) => !c.sentAt);

export function addPlanComment(key: string, c: { quote: string; nth: number; body: string }): void {
  const body = c.body.trim();
  if (!body) return;
  const list = store[key] || [];
  if (list.length >= MAX_COMMENTS) return;
  const next: PlanComment = {
    id: Math.random().toString(36).slice(2, 10) + Date.now().toString(36),
    quote: c.quote.slice(0, MAX_QUOTE),
    nth: Math.max(0, c.nth | 0),
    body,
    ts: Date.now(),
  };
  commit({ ...store, [key]: [...list, next] });
}

export function removePlanComment(key: string, id: string): void {
  const list = store[key];
  if (!list) return;
  const next = list.filter((c) => c.id !== id);
  const copy = { ...store };
  if (next.length) copy[key] = next;
  else delete copy[key];
  commit(copy);
}

/** Mark as sent only what was actually sent. Never call this on failure, so the user can retry. */
export function markPlanCommentsSent(key: string, ids: string[]): void {
  const list = store[key];
  if (!list) return;
  const at = Date.now();
  const mark = new Set(ids);
  commit({ ...store, [key]: list.map((c) => (mark.has(c.id) ? { ...c, sentAt: at } : c)) });
}

export function usePlanComments(key: string | null): PlanComment[] {
  const snap = useSyncExternalStore(subscribe, () => snapshot, () => snapshot);
  return key ? snap[key] || EMPTY : EMPTY;
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** For tests: drop the module state and re-read it from localStorage. */
export function resetPlanCommentsForTest(): void {
  store = load();
  snapshot = store;
}

// formatPlanFeedback builds the collected comments into a single feedback message. Comments have
// no structure on the wire (the CLI's approval dialog accepts exactly one feedback string; the
// VSCode extension likewise folds quote + body into one), so this is the only representation
// available. The quote goes in a blockquote with the remark directly below it. The agent reads
// this text and it is also shown as an utterance in the mirror, so it follows the Console's
// display language (docs/ADR0033, "who reads this string").
export function formatPlanFeedback(comments: PlanComment[], note?: string): string {
  const items = comments.filter((c) => c.body.trim());
  const extra = (note || "").trim();
  if (!items.length) return extra;
  const body = items
    .map((c, i) => {
      const quote = c.quote
        .trim()
        .split("\n")
        .map((l) => "> " + l)
        .join("\n");
      return `${i + 1}.\n${quote}\n\n${c.body.trim()}`;
    })
    .join("\n\n");
  const head = t("mirror.plan_feedback_head", { count: items.length });
  return extra ? `${head}\n\n${body}\n\n${extra}` : `${head}\n\n${body}`;
}

// deliverPlanComments holds only the decision of how to deliver the collected comments and when to
// mark them sent. Delivery itself (respond = /plan-respond's reject, say = an ordinary utterance)
// is injected by the caller, so this can be verified without rendering MirrorView.
//
// The route is decided by the state at the moment the button is pressed:
//   pending  -> respond. Sending free text while the approval dialog is open lets the modal
//     swallow the body and turns Enter into an approval, so it can only arrive safely via the
//     route that closes the dialog with Escape first.
//   otherwise (after a rejection, waiting for input or running in plan mode) -> say (an ordinary
//     utterance).
//   believed pending but already decided (no_plan) -> fall back to say.
//
// Mark as sent only when delivery actually happened. Folding away comments that never arrived
// takes unsent to 0, which removes the send button entirely and leaves the user unable to retype
// them (that also produced an error toast and "sent" at the same time).
export interface PlanRespondLike {
  ok: boolean;
  code?: string;
  delivered?: boolean;
  message?: string;
}

// The failure reason also decides who tells the user:
//   say         - the utterance route was refused. say (= sendPrompt) has already reported the
//                 reason, so reporting again from the caller buries the specific reason under a
//                 generic message.
//   respond     - /plan-respond refused. Show its message verbatim.
//   undelivered - the rejection went through but the body did not land (the composer could not be
//                 confirmed as back).
export type PlanDeliveryResult =
  /** Delivered = the comments are folded. via is the route, feedback the text actually sent (for the echo). */
  | { ok: true; via: "reject" | "prompt"; feedback: string }
  /** Not delivered = nothing was folded. */
  | { ok: false; reason: "say" | "respond" | "undelivered"; message?: string };

export async function deliverPlanComments(
  key: string,
  deps: {
    pending: boolean;
    respond: (feedback: string) => Promise<PlanRespondLike>;
    say: (feedback: string) => Promise<boolean>;
  },
): Promise<PlanDeliveryResult | null> {
  const list = unsentComments(getPlanComments(key));
  if (!list.length) return null; // nothing to send (the button is not shown in this state)
  const feedback = formatPlanFeedback(list);
  const ids = list.map((c) => c.id);
  // The utterance route. Folding without checking say's return value is exactly the failure mode
  // described above, so this return value is the single branch point.
  const bySaying = async (): Promise<PlanDeliveryResult> => {
    if (!(await deps.say(feedback))) return { ok: false, reason: "say" };
    markPlanCommentsSent(key, ids);
    return { ok: true, via: "prompt", feedback };
  };
  if (!deps.pending) return bySaying();
  const res = await deps.respond(feedback);
  if (!res.ok) {
    // Already decided through another route (no_plan): deliver it as a plain utterance.
    if (res.code === "no_plan") return bySaying();
    return { ok: false, reason: "respond", message: res.message };
  }
  // The rejection went through but the feedback did not arrive (composer recovery unconfirmed).
  if (!res.delivered) return { ok: false, reason: "undelivered", message: res.message };
  markPlanCommentsSent(key, ids);
  return { ok: true, via: "reject", feedback };
}
