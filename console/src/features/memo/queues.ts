// Pure logic of the memo section's auto-run side (ADR 0137): reading what the Control Plane
// answers, the words for a message's state and origin, the release cards, and the settings
// form. No React and no module-level state, so nothing leaks between tests.
//
// Vocabulary: members never see the word "queue" (it already names a session's waiting inputs,
// ADR 0105). Every working term lives in one catalogue key (memo.state_*, memo.autorun*, ...),
// so the owner can change a word with one line.
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { Session } from "../../types/session.ts";
import { browserTimeZone, effectiveTimeZone, fmtDateTime, isValidTimeZone } from "../../lib/intl.ts";
import type {
  Memo,
  MemoAfterItem,
  MemoDelivery,
  MemoQueue,
  MemoQueueInput,
  MemoQueueWaiting,
  MemoQueuesWire,
  MemoTidyBlocked,
  MemoTidyRun,
  MemoTidyRunsWire,
  MemoWaitingReason,
} from "../../types/memo.ts";

// Limits the Control Plane enforces (control-plane/memo_queue.go). The form checks them first
// so a mistake is named next to its field; the Control Plane stays the authority.
export const QUEUE_MAX_ACTIVE_MAX = 6;
export const QUEUE_BATCH_MAX = 20;
/** The Control Plane's ceiling for follow-ups of one approved note, and what a queue starts with. */
export const QUEUE_CONTINUATION_MAX = 10;
export const QUEUE_CONTINUATION_DEFAULT = 4;
export const QUEUE_INSTRUCTION_MAX_BYTES = 16 << 10;
export const QUEUE_SPEND_MAX = 10000;
export const QUEUE_TASK_HOURS_MAX = 72;
export const QUEUE_FIRES_PER_DAY_MAX = 200;
export const QUEUE_WINDOW_MAX_MINUTES = 24 * 60;
export const QUEUE_OPEN_AFTER_MAX = 100;
// Most categories one tidy may move a message to (control-plane/store MaxTidyDestinations).
export const QUEUE_TIDY_DESTINATIONS_MAX = 10;
// What a row that never had these gets (the Control Plane's own defaults).
export const QUEUE_TASK_HOURS_DEFAULT = 12;
export const QUEUE_FIRES_PER_DAY_DEFAULT = 20;
export const QUEUE_WINDOW_DEFAULT_MINUTES = 60;

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown, d: number): number => (typeof v === "number" && Number.isFinite(v) ? v : d);
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : []);

const WAITING_REASONS: readonly string[] = [
  "tenant_off",
  "disabled",
  "no_report",
  "no_references",
  "paused",
  "window_closed",
  "below_threshold",
  "quiet_minute",
  "daily_cap",
  "backoff",
  "usage_limit",
  "no_place",
  "tidying",
  "no_tidy",
  "prerequisites",
  "deferred",
  "scheduled",
  "nothing_waiting",
] satisfies MemoWaitingReason[];

/** The `waiting` of a queue: absent from an older Control Plane, and a reason this Console does
 *  not know (a newer Control Plane) is dropped rather than shown as an identifier. */
function readWaiting(v: unknown): MemoQueueWaiting | undefined {
  if (!isObj(v) || typeof v.reason !== "string" || !WAITING_REASONS.includes(v.reason)) return undefined;
  const d = isObj(v.detail) ? v.detail : {};
  return {
    reason: v.reason as MemoWaitingReason,
    until: str(v.until) || undefined,
    detail: {
      needed: num(d.needed, 0),
      waiting: num(d.waiting, 0),
      agentKind: str(d.agentKind),
      by: d.by === "member" ? "member" : "category",
      blocked: num(d.blocked, 0),
      deferred: num(d.deferred, 0),
      scheduled: num(d.scheduled, 0),
    },
  };
}

export function readQueue(v: unknown): MemoQueue | null {
  if (!isObj(v) || typeof v.categoryId !== "string" || !v.categoryId) return null;
  return {
    categoryId: v.categoryId,
    repo: str(v.repo),
    category: str(v.category),
    instruction: str(v.instruction),
    agentKind: str(v.agentKind),
    model: str(v.model),
    effort: str(v.effort),
    route: str(v.route),
    spendCapUsd: num(v.spendCapUsd, 0),
    worktree: v.worktree !== false,
    acceptOwnSessions: v.acceptOwnSessions === true,
    continuationMax: num(v.continuationMax, 4),
    tidy: v.tidy === true,
    tidyDestinations: strings(v.tidyDestinations),
    maxActive: num(v.maxActive, 2),
    batch: num(v.batch, 1),
    maxTaskHours: num(v.maxTaskHours, QUEUE_TASK_HOURS_DEFAULT),
    openCron: str(v.openCron),
    openMinutes: num(v.openMinutes, 0),
    tz: str(v.tz),
    openAfter: num(v.openAfter, 0),
    maxFiresPerDay: num(v.maxFiresPerDay, QUEUE_FIRES_PER_DAY_DEFAULT),
    paused: v.paused === true,
    pausedAt: str(v.pausedAt),
    pausedReason: str(v.pausedReason),
    draining: v.draining === true,
    noReportAt: str(v.noReportAt),
    noRefsAt: str(v.noRefsAt),
    noTidyAt: str(v.noTidyAt),
    enabled: v.enabled === true,
    generation: num(v.generation, 0),
    createdAt: str(v.createdAt),
    updatedAt: str(v.updatedAt),
    waiting: readWaiting(v.waiting),
    fireBlock: readWaiting(v.fireBlock),
  };
}

/** GET /api/memo-queues, normalised. The Control Plane can answer a JSON null where an array is
 *  expected, and an unguarded `.map` there blanks the whole Console (no error boundary), so
 *  every list is rebuilt here. null = the read failed (an error body, an older Control Plane):
 *  the caller keeps what it had. */
export function readMemoQueues(raw: unknown): MemoQueuesWire | null {
  if (!isObj(raw) || raw.error) return null;
  const list = Array.isArray(raw.queues) ? raw.queues : [];
  const queues: MemoQueue[] = [];
  for (const q of list) {
    const r = readQueue(q);
    if (r) queues.push(r);
  }
  return { tenantEnabled: raw.tenantEnabled === true, repoSettings: raw.repoSettings === true, queues };
}

/** GET .../deliveries, normalised the same way. null = the read failed. */
export function readDeliveries(raw: unknown): MemoDelivery[] | null {
  if (raw === null) return [];
  if (isObj(raw) && raw.error) return null;
  if (!Array.isArray(raw)) return isObj(raw) && Array.isArray(raw.deliveries) ? readDeliveries(raw.deliveries) : null;
  const out: MemoDelivery[] = [];
  for (const d of raw) {
    if (!isObj(d) || typeof d.id !== "string") continue;
    out.push({
      id: d.id,
      state: str(d.state),
      outcome: str(d.outcome),
      session: str(d.session),
      messageIds: Array.isArray(d.messageIds) ? d.messageIds.filter((x): x is string => typeof x === "string") : [],
      createdAt: str(d.createdAt),
      sentAt: str(d.sentAt),
      closedAt: str(d.closedAt),
      heldBy: str(d.heldBy),
      heldSession: str(d.heldSession),
      heldSeenAt: str(d.heldSeenAt),
      heldWakers: readHeldWakers(d.heldWakers),
      overdue: d.overdue === true,
      overdueAt: str(d.overdueAt),
      auto: d.auto === true,
      retire: str(d.retire),
      firstTurn: isObj(d.firstTurn) ? { outcome: str(d.firstTurn.outcome), ms: num(d.firstTurn.ms, 0), endedAt: str(d.firstTurn.endedAt) } : null,
      answer: str(d.answer),
      answerClass: str(d.answerClass),
      defer: isObj(d.defer) ? { reason: str(d.defer.reason), notBefore: str(d.defer.notBefore), at: str(d.defer.at), result: str(d.defer.result) } : null,
      returnBlocked: Array.isArray(d.returnBlocked)
        ? d.returnBlocked.filter(isObj).map((b) => ({ id: str(b.id), by: str(b.by) })).filter((b) => b.id !== "")
        : [],
      returnedAt: str(d.returnedAt),
    });
  }
  return out;
}

// ---- why nothing starts ---------------------------------------------------------------------

/** The line that says why a category with notes waiting starts nothing: a catalogue key and its
 *  parameters. Null when there is nothing to say: no `waiting` (a run can start, nothing waits, an
 *  older Control Plane), and for a pause and a workspace that has to be restarted (for runs, for notes that point at an item, or for notes a tidy made), which have
 *  notices of their own that must not be said twice. `at` writes a time the way the screen does. */
export function waitingWords(
  q: Pick<MemoQueue, "waiting">,
  at: (iso: string) => string,
  agentName: (kind: string) => string,
): { key: MsgKey; params?: Record<string, string | number> } | null {
  const w = q.waiting;
  if (!w || w.reason === "nothing_waiting" || w.reason === "paused" || w.reason === "no_report" || w.reason === "no_references" || w.reason === "no_tidy") return null;
  const when = w.until && !Number.isNaN(Date.parse(w.until)) ? { at: at(w.until) } : null;
  switch (w.reason) {
    case "tenant_off":
      return { key: "memo.waiting_tenant_off" };
    case "disabled":
      return { key: "memo.waiting_disabled" };
    case "window_closed":
      return when ? { key: "memo.waiting_window_closed_at", params: when } : { key: "memo.waiting_window_closed" };
    case "below_threshold":
      if (w.detail?.scheduled)
        return {
          key: w.detail.blocked ? "memo.waiting_below_threshold_blocked_timed" : "memo.waiting_below_threshold_timed",
          params: { needed: w.detail.needed ?? 0, count: w.detail.waiting ?? 0, blocked: w.detail.blocked ?? 0, timed: w.detail.scheduled + (w.detail.deferred ?? 0) },
        };
      if (w.detail?.deferred)
        return {
          key: w.detail?.blocked ? "memo.waiting_below_threshold_blocked_deferred" : "memo.waiting_below_threshold_deferred",
          params: { needed: w.detail?.needed ?? 0, count: w.detail?.waiting ?? 0, blocked: w.detail?.blocked ?? 0, deferred: w.detail.deferred },
        };
      return w.detail?.blocked
        ? { key: "memo.waiting_below_threshold_blocked", params: { needed: w.detail?.needed ?? 0, count: w.detail?.waiting ?? 0, blocked: w.detail.blocked } }
        : { key: "memo.waiting_below_threshold", params: { needed: w.detail?.needed ?? 0, count: w.detail?.waiting ?? 0 } };
    case "quiet_minute":
      return when ? { key: "memo.waiting_quiet_at", params: when } : { key: "memo.waiting_quiet" };
    case "daily_cap":
      return when ? { key: "memo.waiting_cap_at", params: when } : { key: "memo.waiting_cap" };
    case "backoff":
      return when ? { key: "memo.waiting_backoff_at", params: when } : { key: "memo.waiting_backoff" };
    case "usage_limit":
      return { key: "memo.waiting_usage_limit", params: { agent: agentName(w.detail?.agentKind ?? "") } };
    case "no_place":
      return { key: w.detail?.by === "member" ? "memo.waiting_no_place_member" : "memo.waiting_no_place" };
    case "tidying":
      return { key: "memo.waiting_tidying" };
    case "prerequisites":
      if (w.detail?.scheduled)
        return {
          key: "memo.waiting_prerequisites_timed",
          params: { count: w.detail.blocked ?? 0, timed: w.detail.scheduled + (w.detail.deferred ?? 0) },
        };
      return w.detail?.deferred
        ? { key: "memo.waiting_prerequisites_deferred", params: { count: w.detail?.blocked ?? 0, deferred: w.detail.deferred } }
        : { key: "memo.waiting_prerequisites", params: { count: w.detail?.blocked ?? 0 } };
    case "deferred":
      // Some of them carry a start time instead: the next time may be one of theirs.
      if (w.detail?.scheduled)
        return when
          ? { key: "memo.waiting_timed_at", params: { ...when, count: w.detail.scheduled + (w.detail.deferred ?? 0) } }
          : { key: "memo.waiting_timed", params: { count: w.detail.scheduled + (w.detail.deferred ?? 0) } };
      return when
        ? { key: "memo.waiting_deferred_at", params: { ...when, count: w.detail?.deferred ?? 0 } }
        : { key: "memo.waiting_deferred", params: { count: w.detail?.deferred ?? 0 } };
    case "scheduled":
      return when
        ? { key: "memo.waiting_scheduled_at", params: { ...when, count: w.detail?.scheduled ?? 0 } }
        : { key: "memo.waiting_scheduled", params: { count: w.detail?.scheduled ?? 0 } };
  }
}

/** Why "Run now" is unavailable, as a catalogue key and its parameters; null when a press may start
 *  a run. Advisory: it is the list's fireBlock (what the claim would refuse for), so no rule of the
 *  Control Plane is copied here, and the press itself stays the authority. A queue the list has not
 *  read (or an older Control Plane) leaves it enabled. */
export function fireBlockWords(
  q: Pick<MemoQueue, "fireBlock"> | undefined,
  at: (iso: string) => string,
  agentName: (kind: string) => string,
): { key: MsgKey; params?: Record<string, string | number> } | null {
  const b = q?.fireBlock;
  if (!b) return null;
  if (b.reason === "nothing_waiting") return { key: "memo.d_fire_none" };
  return waitingWords({ waiting: b }, at, agentName);
}

/** Reasons that stop the queue for long or need the member to act; the rest is routine scheduling. */
const WARN_REASONS: readonly string[] = ["daily_cap", "usage_limit", "backoff", "no_place", "tenant_off"];

/** Whether the "why nothing starts" line should be drawn as a warning rather than muted text. */
export function waitingIsWarning(q: Pick<MemoQueue, "waiting">): boolean {
  return !!q.waiting && WARN_REASONS.includes(q.waiting.reason);
}

// ---- notes that wait for other notes (ADR 0137, addendum 2026-11-19) -------------------------

const AFTER_REASONS = [
  "held",
  "pending",
  "running",
  "first_turn",
  "deleted",
  "moved",
  "unconfirmed",
  "superseded",
  "hand_sent",
  "aborted",
  "unknown",
  "not_clean",
  "bad_first_turn",
  "overdue",
  "no_first_turn",
  "pr",
  "pr_closed",
  "no_pr",
  "session_gone",
  "answer",
  "not_done",
] as const;

/** The words for how a note another waits for stands. A reason this Console does not know (a newer
 *  Control Plane) falls back to the state alone. */
export function afterItemWords(it: Pick<MemoAfterItem, "state" | "reason">): MsgKey {
  if (it.state === "satisfied") return "memo.after_r_done";
  if (it.reason && (AFTER_REASONS as readonly string[]).includes(it.reason)) return `memo.after_r_${it.reason}` as MsgKey;
  return it.state === "failed" ? "memo.after_r_failed" : "memo.after_r_waiting";
}

/** The words for why a follow-up note was not accepted without approval: a heldReason whose
 *  reason is continuation_<why>. Null for any other reason. */
export function continuationHeldKey(reason: string | undefined): MsgKey | null {
  if (!reason || !reason.startsWith("continuation_")) return null;
  const why = reason.slice("continuation_".length);
  return (CONTINUATION_REASONS as readonly string[]).includes(why) ? (`memo.cont_r_${why}` as MsgKey) : "memo.cont_r_other";
}
const CONTINUATION_REASONS = ["origin", "delivery", "batch", "given", "reference", "root", "off", "used", "limit", "stopped"] as const;

/** The words for a held reason that is not about a prerequisite: today only a run recipe the
 *  category's settings cannot create a run with (`recipe_invalid`). Null for any other reason. */
export function heldRecipeKey(reason: string | undefined): MsgKey | null {
  return reason === "recipe_invalid" ? "memo.held_recipe_invalid" : null;
}

/** The run recipe a message asks for; every field "" when it follows the category's setting. */
export function memoRecipe(m: Pick<Memo, "agentKind" | "model" | "effort" | "route">) {
  return { agentKind: m.agentKind ?? "", model: m.model ?? "", effort: m.effort ?? "", route: m.route ?? "" };
}

/** True when the message overrides any field of the category's setting. */
export const hasRecipe = (m: Pick<Memo, "agentKind" | "model" | "effort" | "route">): boolean => Object.values(memoRecipe(m)).some((v) => v !== "");

/** Equal for two messages exactly when the Control Plane's stored recipes are equal (it compares
 *  the canonical encoding; this is the same fields, so the tidy preview groups as it applies). */
export const recipeKey = (m: Pick<Memo, "agentKind" | "model" | "effort" | "route">): string => {
  const r = memoRecipe(m);
  return hasRecipe(m) ? [r.agentKind, r.model, r.effort, r.route].join("\x00") : "";
};

/** The distinct own recipes among the messages a run took, one message each. Only a message still
 *  bound to that run counts: one returned to the queue may have been edited since, and its current
 *  recipe says nothing about what the run used. */
export function runRecipes(messages: readonly Memo[], d: Pick<MemoDelivery, "id" | "messageIds">): Memo[] {
  const seen = new Set<string>();
  const out: Memo[] = [];
  for (const m of messages) {
    if (m.deliveryId !== d.id || !d.messageIds.includes(m.id) || !hasRecipe(m) || seen.has(recipeKey(m))) continue;
    seen.add(recipeKey(m));
    out.push(m);
  }
  return out;
}

/** The parts of a recipe worth a badge, in reading order: agent, model, effort, route. */
export function recipeParts(m: Pick<Memo, "agentKind" | "model" | "effort" | "route">): string[] {
  const r = memoRecipe(m);
  return [r.agentKind, r.model, r.effort, r.route].filter((v) => v !== "");
}

/** The PR number of a key "owner/repo#N", with its # ("#N"), else the key. */
const prNumberOf = (key: string) => {
  const i = key.lastIndexOf("#");
  return i >= 0 ? key.slice(i) : key;
};

/** The words for how a note another waits for stands, with the PRs it waits for when its run's PRs
 *  are not all merged yet ("waiting for PR #12 to merge"). */
export function afterItemMessage(it: Pick<MemoAfterItem, "state" | "reason" | "prs" | "answer">): { key: MsgKey; params?: Record<string, string | number> } {
  if (it.state === "waiting" && it.reason === "pr") {
    const open = (it.prs ?? []).filter((p) => p.state !== "merged");
    if (open.length > 0) return { key: "memo.after_r_pr_merge", params: { prs: open.map((p) => prNumberOf(p.key)).join(", ") } };
  }
  if (it.state === "failed" && it.reason === "pr_closed") {
    const closed = (it.prs ?? []).filter((p) => p.state === "closed");
    if (closed.length > 0) return { key: "memo.after_r_pr_closed_named", params: { prs: closed.map((p) => prNumberOf(p.key)).join(", ") } };
  }
  // The head is what the run said instead of Done:, so the member sees why it counts as unfinished.
  // afterStatus reaches the list unparsed, so the head is checked for a string here.
  const answer = typeof it.answer === "string" ? it.answer.trim() : "";
  if (it.state === "failed" && it.reason === "not_done" && answer) {
    return { key: "memo.after_r_not_done_named", params: { answer } };
  }
  return { key: afterItemWords(it) };
}

/** A short name for a note: its work-item key, else the first line of its text. */
export function afterLabelOf(m: Pick<Memo, "reference" | "body" | "refPath">): string {
  if (m.reference?.key) return m.reference.key;
  const line = (m.body || m.refPath || "").trim().split("\n")[0];
  return line.length > 60 ? line.slice(0, 60) + "…" : line;
}

/** The notes of m's category that m may wait for, in the category's order (created time, then
 *  position, then id, as the Control Plane runs them): earlier and later ones alike, never itself,
 *  one replaced by a tidy, or one that already waits for m directly or through others. The last
 *  would make the two wait for each other, which the Control Plane refuses (memo_after_invalid);
 *  this list only sees the loaded notes, so the Control Plane's check stays the authority. */
export function afterChoices(memos: Memo[], m: Memo): { id: string; label: string }[] {
  const before = (a: Memo, b: Memo) =>
    a.createdAt !== b.createdAt ? a.createdAt < b.createdAt : a.position !== b.position ? a.position < b.position : a.id < b.id;
  const byId = new Map(memos.map((o) => [o.id, o]));
  const waitsForM = (start: Memo): boolean => {
    const seen = new Set<string>();
    const stack = [...(start.after ?? [])];
    while (stack.length > 0) {
      const id = stack.pop()!;
      if (id === m.id) return true;
      if (seen.has(id)) continue;
      seen.add(id);
      stack.push(...(byId.get(id)?.after ?? []));
    }
    return false;
  };
  return memos
    .filter((o) => o.id !== m.id && o.repo === m.repo && o.category === m.category && o.state && o.state !== "superseded" && !waitsForM(o))
    .sort((a, b) => (before(a, b) ? -1 : 1))
    .map((o) => ({ id: o.id, label: afterLabelOf(o) || o.id }));
}

/** One candidate (or chosen) prerequisite as the picker names it: enough to tell two notes of the
 *  same work item apart. `memo` is absent for a chosen note this list no longer holds. */
export interface AfterEntry {
  id: string;
  memo?: Memo;
  /** The reference key; "" when the note has none. */
  key: string;
  /** The work item's title when the cache has it, else the first line of the note's text. */
  title: string;
  /** The note's whole text, for the tooltip and the search. */
  text: string;
  /** Why a chosen note is no longer on offer (deleted, moved, replaced ...). */
  why?: MsgKey;
}

/** Builds the picker's name for a note. `fallback` names it when the note itself is not loaded
 *  (the label the Control Plane gave it); `workTitle` is the cached title of its work item, "". */
export function afterEntryOf(id: string, memo: Memo | undefined, fallback: string, workTitle: string): AfterEntry {
  if (!memo) return { id, key: "", title: fallback || id, text: "" };
  const text = (memo.body || memo.refPath || "").trim();
  const line = text.split("\n")[0];
  const key = memo.reference?.key ?? "";
  const title = workTitle.trim() || (line.length > 80 ? line.slice(0, 80) + "…" : line);
  return { id, memo, key, title: key || title ? title : id, text };
}

/** The entries whose key, title or text contain every word of the query (case-folded), at most
 *  `limit` of them in the order given, and how many matched in all. An empty query matches all. */
export function searchAfterEntries(entries: readonly AfterEntry[], query: string, limit: number): { items: AfterEntry[]; total: number } {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (e: AfterEntry) => {
    const hay = (e.key + "\n" + e.title + "\n" + e.text).toLowerCase();
    return words.every((w) => hay.includes(w));
  };
  const all = entries.filter(hit);
  return { items: all.slice(0, limit), total: all.length };
}

// ---- message state and origin ------------------------------------------------------------

const STATE_KEYS: Record<string, [MsgKey, MsgKey]> = {
  held: ["memo.state_held", "memo.state_held_title"],
  pending: ["memo.state_pending", "memo.state_pending_title"],
  claimed: ["memo.state_claimed", "memo.state_claimed_title"],
  delivered: ["memo.state_delivered", "memo.state_delivered_title"],
  unconfirmed: ["memo.state_unconfirmed", "memo.state_unconfirmed_title"],
  superseded: ["memo.state_superseded", "memo.state_superseded_title"],
};

/** The words for a message's state; null for a plain note (state "" or absent) and for a state
 *  this Console does not know, which is better left unlabelled than shown as an identifier. */
export function memoStateWords(state: string | undefined): { label: MsgKey; title: MsgKey } | null {
  const k = state ? STATE_KEYS[state] : undefined;
  return k ? { label: k[0], title: k[1] } : null;
}

/** Where a message in a queue came from, as a catalogue key plus its parameter. Plain notes
 *  say nothing: their provenance is not interesting (old rows read "workspace"). */
export function memoOriginWords(m: Pick<Memo, "state" | "provenance" | "producer">): { key: MsgKey; name?: string } | null {
  if (!m.state) return null;
  const who = (m.producer ?? "").trim();
  switch (m.provenance) {
    case "member":
      return { key: "memo.origin_member" };
    case "workspace":
      return who ? { key: "memo.origin_session", name: who } : { key: "memo.origin_session_unknown" };
    case "mcp":
      return who ? { key: "memo.origin_mcp", name: who } : { key: "memo.origin_mcp_unknown" };
    case "curator":
      return { key: "memo.origin_curator" };
    default:
      return null;
  }
}

// ---- release cards -----------------------------------------------------------------------

/** One card: the held messages one producer put into one category. */
export interface ReleaseCard {
  key: string;
  provenance: string;
  producer: string;
  items: Memo[];
}

/** The held messages of one group as release cards, one per producer (a session, an /mcp
 *  connection). Everything here is what the card will show, in the order it shows it. */
export function releaseCards(memos: Memo[]): ReleaseCard[] {
  const out = new Map<string, ReleaseCard>();
  for (const m of memos) {
    if (m.state !== "held" || !m.contentHash) continue;
    const provenance = m.provenance ?? "";
    const producer = (m.producer ?? "").trim();
    const key = provenance + "\x00" + producer;
    let c = out.get(key);
    if (!c) {
      c = { key, provenance, producer, items: [] };
      out.set(key, c);
    }
    c.items.push(m);
  }
  return [...out.values()];
}

/** The ids a failed release named as changed. A 409 carries them in error.stale; anything else
 *  yields none. */
export function staleIdsOf(res: unknown): string[] {
  const e = isObj(res) && isObj(res.error) ? res.error : null;
  if (!e || e.code !== "memo_release_stale") return [];
  return Array.isArray(e.stale) ? e.stale.filter((x): x is string => typeof x === "string") : [];
}

export const isStaleRelease = (res: unknown): boolean =>
  isObj(res) && isObj(res.error) && res.error.code === "memo_release_stale";

// ---- settings form -----------------------------------------------------------------------

/** The three ways a category runs by itself (ADR 0137 §6). */
export type RunMode = "always" | "window" | "threshold";

/** The form's state: numbers stay strings while typed, so "" and "1e" are representable. */
export interface QueueDraft {
  instruction: string;
  agentKind: string;
  model: string;
  effort: string;
  spendCapUsd: string;
  worktree: boolean;
  acceptOwnSessions: boolean;
  continuationMax: string;
  tidy: boolean;
  tidyDestinations: string[];
  maxActive: string;
  batch: string;
  runMode: RunMode;
  openCron: string;
  openMinutes: string;
  tz: string;
  openAfter: string;
  maxFiresPerDay: string;
  maxTaskHours: string;
  enabled: boolean;
}

export const runModeOf = (q: Pick<MemoQueue, "openCron" | "openAfter"> | undefined): RunMode =>
  q && q.openCron ? "window" : q && q.openAfter > 0 ? "threshold" : "always";

export function draftOf(q: MemoQueue | undefined): QueueDraft {
  const mode = runModeOf(q);
  return {
    instruction: q?.instruction ?? "",
    agentKind: q?.agentKind || "claude",
    model: q?.model ?? "",
    effort: q?.effort ?? "",
    spendCapUsd: String(q?.spendCapUsd ?? 0),
    worktree: q?.worktree ?? true,
    acceptOwnSessions: q?.acceptOwnSessions ?? false,
    continuationMax: String(q?.continuationMax ?? QUEUE_CONTINUATION_DEFAULT),
    tidy: q?.tidy ?? false,
    tidyDestinations: q?.tidyDestinations ?? [],
    maxActive: String(q?.maxActive ?? 2),
    batch: String(q?.batch ?? 1),
    runMode: mode,
    openCron: q?.openCron ?? "",
    openMinutes: String(q && q.openMinutes > 0 ? q.openMinutes : QUEUE_WINDOW_DEFAULT_MINUTES),
    // A workspace's zone is not known here: a window the person has not set yet follows the
    // browser. A window that is saved keeps what it was saved with (empty = UTC).
    tz: mode === "window" ? (q?.tz ?? "") : browserTimeZone(),
    openAfter: String(q && q.openAfter > 0 ? q.openAfter : 3),
    maxFiresPerDay: String(q?.maxFiresPerDay ?? QUEUE_FIRES_PER_DAY_DEFAULT),
    maxTaskHours: String(q?.maxTaskHours ?? QUEUE_TASK_HOURS_DEFAULT),
    enabled: q?.enabled ?? false,
  };
}

export type DraftField =
  | "instruction"
  | "spendCapUsd"
  | "maxActive"
  | "batch"
  | "openCron"
  | "openMinutes"
  | "tz"
  | "openAfter"
  | "maxFiresPerDay"
  | "maxTaskHours"
  | "continuationMax"
  | "tidyDestinations";
export type DraftErrors = Partial<Record<DraftField, MsgKey>>;

const wholeIn = (s: string, lo: number, hi: number): boolean => {
  const t = s.trim();
  return /^\d+$/.test(t) && Number(t) >= lo && Number(t) <= hi;
};

/** What the form can say before the Control Plane does. One key per field. Only the fields of the
 *  chosen way to run are checked: the others are not sent. The cron field is checked for its
 *  shape alone (five fields); the Control Plane parses it and its refusal is shown as it comes. */
export function draftErrors(d: QueueDraft): DraftErrors {
  const e: DraftErrors = {};
  const bytes = new TextEncoder().encode(d.instruction.trim()).length;
  if (bytes > QUEUE_INSTRUCTION_MAX_BYTES) e.instruction = "memo.q_err_instruction_long";
  else if (d.enabled && bytes === 0) e.instruction = "memo.q_err_instruction";
  const spend = d.spendCapUsd.trim();
  if (!/^\d+(\.\d+)?$/.test(spend) || Number(spend) > QUEUE_SPEND_MAX) e.spendCapUsd = "memo.q_err_budget";
  if (!wholeIn(d.maxActive, 1, QUEUE_MAX_ACTIVE_MAX)) e.maxActive = "memo.q_err_max_active";
  if (!wholeIn(d.batch, 1, QUEUE_BATCH_MAX)) e.batch = "memo.q_err_batch";
  if (!wholeIn(d.continuationMax, 0, QUEUE_CONTINUATION_MAX)) e.continuationMax = "memo.q_err_continuation";
  if (!wholeIn(d.maxFiresPerDay, 1, QUEUE_FIRES_PER_DAY_MAX)) e.maxFiresPerDay = "memo.q_err_fires";
  if (!wholeIn(d.maxTaskHours, 1, QUEUE_TASK_HOURS_MAX)) e.maxTaskHours = "memo.q_err_task_hours";
  if (d.tidyDestinations.length > QUEUE_TIDY_DESTINATIONS_MAX) e.tidyDestinations = "memo.q_err_tidy_destinations";
  if (d.runMode === "window") {
    if (d.openCron.trim().split(/\s+/).filter(Boolean).length !== 5) e.openCron = "memo.q_err_cron";
    if (!wholeIn(d.openMinutes, 1, QUEUE_WINDOW_MAX_MINUTES)) e.openMinutes = "memo.q_err_window";
    if (d.tz.trim() !== "" && !isValidTimeZone(d.tz.trim())) e.tz = "memo.q_err_tz";
  } else if (d.runMode === "threshold") {
    if (!wholeIn(d.openAfter, 1, QUEUE_OPEN_AFTER_MAX)) e.openAfter = "memo.q_err_open_after";
  }
  return e;
}

/** The PUT body. A PUT replaces the whole row, so every setting is sent: the route, which this
 *  form does not edit, as it was, and the fields of a way to run that was not chosen as their
 *  "off" values (the Control Plane clears them the same way). The read-only state (paused,
 *  noReportAt, ...) is never part of it. Only call with no errors. */
export function draftBody(d: QueueDraft, route: string): MemoQueueInput {
  const window = d.runMode === "window";
  return {
    instruction: d.instruction.trim(),
    agentKind: d.agentKind.trim(),
    model: d.model.trim(),
    effort: d.effort.trim(),
    route,
    spendCapUsd: Number(d.spendCapUsd.trim()),
    worktree: d.worktree,
    acceptOwnSessions: d.acceptOwnSessions,
    continuationMax: Number(d.continuationMax.trim()),
    tidy: d.tidy,
    // Always the current choice, also with the switch off: a PUT replaces the row, and an omitted
    // list would forget what the member picked.
    tidyDestinations: d.tidyDestinations,
    maxActive: Number(d.maxActive.trim()),
    batch: Number(d.batch.trim()),
    maxTaskHours: Number(d.maxTaskHours.trim()),
    openCron: window ? d.openCron.trim().split(/\s+/).join(" ") : "",
    openMinutes: window ? Number(d.openMinutes.trim()) : 0,
    tz: window ? d.tz.trim() : "",
    openAfter: d.runMode === "threshold" ? Number(d.openAfter.trim()) : 0,
    maxFiresPerDay: Number(d.maxFiresPerDay.trim()),
    enabled: d.enabled,
  };
}

// ---- what a run is waiting for, and how it went --------------------------------------------

const HELD_KEYS: Record<string, MsgKey> = {
  machine: "memo.d_held_machine",
  person: "memo.d_held_person",
  limit: "memo.d_held_limit",
  wakers: "memo.d_held_wakers",
  unknown: "memo.d_held_unknown",
};

/** Why a run that is still open keeps its place. null = nothing to say: the run is not open, or the
 *  Agent has not reported (an older one). A reason this Console does not know reads as unknown. */
export function heldWords(d: Pick<MemoDelivery, "state" | "heldBy">): MsgKey | null {
  if (d.state !== "open" || !d.heldBy) return null;
  return HELD_KEYS[d.heldBy] ?? "memo.d_held_unknown";
}

/** The watches named in a wakers hold, tolerating a missing or malformed field. */
function readHeldWakers(v: unknown): MemoDelivery["heldWakers"] {
  if (!Array.isArray(v)) return [];
  const out: MemoDelivery["heldWakers"] = [];
  for (const h of v) {
    const id = isObj(h) ? str(h.id).trim() : "";
    if (isObj(h) && id !== "") out.push({ id, source: str(h.source).trim(), timer: h.timer === true, builtin: h.builtin === true });
  }
  return out;
}

/** "id (source), …" for a run held by a watch; "" when it is not, or none was named. */
export function heldNames(d: Pick<MemoDelivery, "state" | "heldBy" | "heldWakers">): string {
  if (d.state !== "open" || d.heldBy !== "wakers" || !Array.isArray(d.heldWakers)) return "";
  return d.heldWakers.map((h) => (h.source ? `${h.id} (${h.source})` : h.id)).join(", ");
}

const TURN_KEYS: Record<string, MsgKey> = {
  clean: "memo.d_turn_clean",
  "turn-failed": "memo.d_turn_failed",
  "turn-aborted": "memo.d_turn_aborted",
};

const ANSWER_KEYS: Record<string, MsgKey> = {
  done: "memo.d_answer_done",
  partial: "memo.d_answer_partial",
  other: "memo.d_answer_other",
  unknown: "memo.d_answer_unknown",
};

/** How the last answer of a run's session reads (#4876): its class's words and the first line it
 *  began with ("" when none could be read). Null while it is not read, and for a run that started
 *  no session. A class this Console does not know reads as a plain last answer. */
export function answerWords(d: Pick<MemoDelivery, "answer" | "answerClass">): { key: MsgKey; head: string; done: boolean } | null {
  if (!d.answerClass) return null;
  return { key: ANSWER_KEYS[d.answerClass] ?? "memo.d_answer_plain", head: (d.answer ?? "").trim(), done: d.answerClass === "done" };
}

/** How the first turn of a run's session ended; null until it did. An outcome the kind does not
 *  record ("") reads as ended. */
export function firstTurnWords(d: Pick<MemoDelivery, "firstTurn">): MsgKey | null {
  if (!d.firstTurn) return null;
  return TURN_KEYS[d.firstTurn.outcome] ?? "memo.d_turn_ended";
}

// ---- notes that changed under an open card -------------------------------------------------

/** What a card has shown of one held note: the hash it first showed, the one shown last, whether
 *  the hash ever moved (latched: going back to the first hash does not clear it, because the
 *  person may have read the middle one), and the hash the person confirmed reading. */
export interface ShownNote {
  first: string;
  last: string;
  changed: boolean;
  ack: string;
}

// From these states a note cannot come back to the card. Pending can: a session that edits an
// approved note puts it back to held with new text, which the person has not read.
const consumed = (state: string | undefined): boolean => state === "claimed" || state === "delivered" || state === "unconfirmed" || state === "superseded";

/** Advances the records by one list. Every move to another hash latches `changed` and drops the
 *  confirmation, so a confirmed hash is never reused after the text moved on. A record lives until
 *  the person approves the note (forgetShown), the note is consumed by a run, or it is gone from the
 *  list; a note that is not held for a moment and then is again keeps it. `inScope` limits what this call may forget to one tenant's keys. */
export function trackShown(
  prev: ReadonlyMap<string, ShownNote>,
  memos: readonly Memo[],
  keyOf: (id: string) => string,
  inScope: (key: string) => boolean,
): Map<string, ShownNote> {
  const next = new Map(prev);
  const byKey = new Map(memos.map((m) => [keyOf(m.id), m]));
  for (const key of prev.keys()) {
    if (!inScope(key)) continue;
    const m = byKey.get(key);
    if (!m || consumed(m.state)) next.delete(key);
  }
  for (const [key, m] of byKey) {
    if (m.state !== "held" || !m.contentHash) continue;
    const r = next.get(key);
    if (!r) next.set(key, { first: m.contentHash, last: m.contentHash, changed: false, ack: "" });
    else if (r.last !== m.contentHash) next.set(key, { ...r, last: m.contentHash, changed: true, ack: "" });
  }
  return next;
}

/** The notes marked as changed, and those whose current text the person has not confirmed. */
export function shownStatus(
  recs: ReadonlyMap<string, ShownNote>,
  memos: readonly Memo[],
  keyOf: (id: string) => string,
): { edited: Set<string>; awaiting: Set<string> } {
  const edited = new Set<string>();
  const awaiting = new Set<string>();
  for (const m of memos) {
    const r = recs.get(keyOf(m.id));
    if (m.state !== "held" || !r || !r.changed) continue;
    edited.add(m.id);
    if (r.ack !== m.contentHash) awaiting.add(m.id);
  }
  return { edited, awaiting };
}

/** The given notes were approved by the person: their records are done. */
export function forgetShown(prev: ReadonlyMap<string, ShownNote>, items: readonly Memo[], keyOf: (id: string) => string): Map<string, ShownNote> {
  const next = new Map(prev);
  for (const m of items) next.delete(keyOf(m.id));
  return next;
}

/** The person has read what is shown now: the records of the given notes take their current hash. */
export function confirmShown(prev: ReadonlyMap<string, ShownNote>, items: readonly Memo[], keyOf: (id: string) => string): Map<string, ShownNote> {
  const next = new Map(prev);
  for (const m of items) {
    const r = next.get(keyOf(m.id));
    if (r && r.changed && m.contentHash) next.set(keyOf(m.id), { ...r, ack: m.contentHash });
  }
  return next;
}

// ---- tidying ---------------------------------------------------------------------------------

/** What a row says about a tidy run's part in a message: a catalogue key and its parameters. */
export interface TidyMark {
  key: MsgKey;
  params?: Record<string, string | number>;
  /** The longer explanation, for a tooltip. */
  title?: MsgKey;
}

/** The marks a message carries from a tidy run, in the order a row shows them: that it was made from
 *  other notes (and whose), that it was moved from another category. The replaced state and the
 *  release mark have their own words. An id list that is empty reads as not derived. */
export function tidyMarks(m: Pick<Memo, "derivedFrom" | "sourceProvenance" | "producer" | "tidyFrom">): TidyMark[] {
  const out: TidyMark[] = [];
  const count = m.derivedFrom?.length ?? 0;
  if (count > 0) {
    const who = (m.producer ?? "").trim();
    switch (m.sourceProvenance) {
      case "member":
        out.push({ key: "memo.tidy_derived_member", params: { count } });
        break;
      case "workspace":
        out.push(who ? { key: "memo.tidy_derived_session", params: { count, name: who } } : { key: "memo.tidy_derived_session_unknown", params: { count } });
        break;
      case "mcp":
        out.push(who ? { key: "memo.tidy_derived_mcp", params: { count, name: who } } : { key: "memo.tidy_derived_mcp_unknown", params: { count } });
        break;
      default:
        out.push({ key: "memo.tidy_derived", params: { count } });
    }
  }
  for (const k of out) k.title = "memo.tidy_derived_title";
  const from = m.tidyFrom;
  if (from && from.id) {
    // A category that is gone, or an answer that carries no names, still says that it was moved.
    out.push(from.name ? { key: "memo.tidy_moved_from", params: { name: from.repo ? from.repo + " / " + from.name : from.name }, title: "memo.tidy_moved_title" } : { key: "memo.tidy_moved_unknown", title: "memo.tidy_moved_title" });
  }
  return out;
}

/** The released-by-tidy mark sits next to the "accepted without approval" one: only a pending
 *  message was put there by a run. */
export const releasedByTidy = (m: Pick<Memo, "state" | "releasedBy">): boolean => m.state === "pending" && m.releasedBy === "tidy";

/** A replaced note is read-only: the Control Plane locks it, so the row offers no edit or send. */
export const isSuperseded = (m: Pick<Memo, "state">): boolean => m.state === "superseded";

const strList = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const count0 = (v: unknown): number => Math.max(0, Math.trunc(num(v, 0)));

function readBlocked(v: unknown): MemoTidyBlocked[] {
  if (!Array.isArray(v)) return [];
  const out: MemoTidyBlocked[] = [];
  for (const b of v) if (isObj(b) && typeof b.id === "string") out.push({ id: b.id, why: str(b.why) });
  return out;
}

function readTidyRun(v: unknown): MemoTidyRun | null {
  if (!isObj(v) || typeof v.id !== "string" || !v.id) return null;
  return {
    id: v.id,
    trigger: str(v.trigger),
    state: str(v.state),
    reason: str(v.reason),
    createdAt: str(v.createdAt),
    finishedAt: str(v.finishedAt),
    undoneAt: str(v.undoneAt),
    merged: count0(v.merged),
    split: count0(v.split),
    moved: count0(v.moved),
    held: count0(v.held),
    sourceIds: strList(v.sourceIds),
    resultIds: strList(v.resultIds),
    movedIds: strList(v.movedIds),
    undoable: v.undoable === true,
    blocked: readBlocked(v.blocked),
  };
}

/** GET .../tidy-runs, normalised. null = the read failed (an error body, a Control Plane without it). */
export function readTidyRuns(raw: unknown): MemoTidyRunsWire | null {
  if (!isObj(raw) || raw.error) return null;
  const runs: MemoTidyRun[] = [];
  for (const r of Array.isArray(raw.runs) ? raw.runs : []) {
    const run = readTidyRun(r);
    if (run) runs.push(run);
  }
  return { runs, usedToday: count0(raw.usedToday), dailyCap: count0(raw.dailyCap) };
}

const TIDY_STATE_KEYS: Record<string, MsgKey> = {
  running: "memo.tidy_state_running",
  applied: "memo.tidy_state_applied",
  kept: "memo.tidy_state_kept",
  dropped: "memo.tidy_state_dropped",
  skipped: "memo.tidy_state_skipped",
  failed: "memo.tidy_state_failed",
  undone: "memo.tidy_state_undone",
};

/** The word for a run's state; null for one this Console does not know. */
export const tidyStateKey = (state: string): MsgKey | null => TIDY_STATE_KEYS[state] ?? null;

// The reasons a run ends without a change that are not a refusal of the model's reply.
const TIDY_REASON_KEYS: Record<string, MsgKey> = {
  stale_input: "memo.tidy_why_stale_input",
  no_queue: "memo.tidy_why_no_queue",
  takeover: "memo.tidy_why_takeover",
  no_marker: "memo.tidy_why_no_marker",
  agent_unreachable: "memo.tidy_why_unreachable",
  wake_failed: "memo.tidy_why_wake_failed",
  no_workspace: "memo.tidy_why_no_workspace",
  timeout: "memo.tidy_why_timeout",
  call_failed: "memo.tidy_why_call_failed",
  agent_error: "memo.tidy_why_call_failed",
};
// The Control Plane's checks of the model's reply (control-plane/internal/memotidy): one word for
// all of them, since the member cannot act on which rule it broke.
const TIDY_REFUSED: readonly string[] = [
  "bad_shape",
  "too_many_operations",
  "unknown_id",
  "duplicate_id",
  "merge_sources",
  "split_parts",
  "not_mergeable",
  "mixed_group",
  "bad_text",
  "text_too_long",
  "text_envelope",
  "text_growth",
  "bad_destination",
  "move_self",
  "duplicate_ref",
  "already_moved",
  "keep_only",
];

/** Why a run that changed nothing ended, in words; null when it has no reason or one this Console
 *  does not know (better silent than an identifier). */
export function tidyReasonKey(r: Pick<MemoTidyRun, "state" | "reason">): MsgKey | null {
  if (!r.reason || r.state === "applied" || r.state === "kept" || r.state === "undone") return null;
  if (TIDY_REASON_KEYS[r.reason]) return TIDY_REASON_KEYS[r.reason];
  return TIDY_REFUSED.includes(r.reason) ? "memo.tidy_why_refused" : null;
}

/** What an applied run did, as the words of the parts that are not zero. */
export function tidyCountMarks(r: Pick<MemoTidyRun, "merged" | "split" | "moved" | "held">): TidyMark[] {
  const out: TidyMark[] = [];
  if (r.merged > 0) out.push({ key: "memo.tidy_n_merged", params: { count: r.merged } });
  if (r.split > 0) out.push({ key: "memo.tidy_n_split", params: { count: r.split } });
  if (r.moved > 0) out.push({ key: r.held > 0 ? "memo.tidy_n_moved_held" : "memo.tidy_n_moved", params: { count: r.moved, held: r.held } });
  return out;
}

const UNDO_WHY_KEYS: Record<string, MsgKey> = {
  claimed: "memo.tidy_block_claimed",
  sent: "memo.tidy_block_sent",
  changed: "memo.tidy_block_changed",
  gone: "memo.tidy_block_gone",
  later_run: "memo.tidy_block_later_run",
  duplicate_ref: "memo.tidy_block_duplicate_ref",
};

/** The messages that keep a run from being undone, grouped by reason (first seen first), each
 *  with its words. A reason this Console does not know reads as changed: the nearest true word. */
export function tidyBlockedGroups(blocked: readonly MemoTidyBlocked[]): { why: string; key: MsgKey; count: number }[] {
  const by = new Map<string, number>();
  for (const b of blocked) {
    const why = UNDO_WHY_KEYS[b.why] ? b.why : "changed";
    by.set(why, (by.get(why) ?? 0) + 1);
  }
  return [...by].map(([why, count]) => ({ why, key: UNDO_WHY_KEYS[why], count }));
}

/** The blocked messages of an undo the Control Plane refused (409 memo_tidy_not_undoable); null for
 *  any other answer, so the caller shows that error as it comes. */
export function undoRefusal(res: unknown): MemoTidyBlocked[] | null {
  const e = isObj(res) && isObj(res.error) ? res.error : null;
  if (!e || e.code !== "memo_tidy_not_undoable") return null;
  return readBlocked(e.blocked);
}

// ---- deliveries --------------------------------------------------------------------------

/** The word for a delivery's state and outcome (ADR 0137 §1): claimed and sending read as
 *  starting; open as running; closed by its outcome. */
export function deliveryStateKey(d: Pick<MemoDelivery, "state" | "outcome">): MsgKey | null {
  switch (d.state) {
    case "claimed":
    case "sending":
      return "memo.d_state_starting";
    case "open":
      return "memo.d_state_running";
    case "closed":
      switch (d.outcome) {
        case "clean":
          return "memo.d_state_clean";
        case "deferred":
          return "memo.d_state_deferred";
        case "failed":
          return "memo.d_state_failed";
        case "aborted":
          return "memo.d_state_aborted";
        case "ended":
          return "memo.d_state_ended";
        case "unknown":
          return "memo.d_state_unknown";
        case "not_sent":
          return "memo.d_state_not_sent";
      }
      return null;
  }
  return null;
}

/** Waiting to run, for the fire button: the messages a fire could take. */
export const pendingCount = (memos: Memo[]): number => memos.filter((m) => m.state === "pending" && !m.sentAt).length;

/** Waiting for the member's approval: held notes, "Changed" ones included. A held state exists only
 * in an auto-run category, and a note already waiting to run (pending) has been approved. */
export const approvalCount = (memos: Memo[]): number => memos.filter((m) => m.state === "held" && !m.sentAt).length;

// ---- a run that hands its note back (ADR 0137, addendum 2026-11-19, part B) ----------------

const DEFER_RESULT_KEYS: Record<string, MsgKey> = {
  applied: "memo.d_defer_applied",
  turn: "memo.d_defer_dropped_turn",
  pr: "memo.d_defer_dropped_pr",
  unread: "memo.d_defer_dropped_unread",
  released: "memo.d_defer_dropped_released",
  returned: "memo.d_defer_returned",
};

/** Whether a run's hand-back is still to come: asked on an open run, or closed as deferred and
 *  waiting for its session to be stopped before the notes go back. */
export function deferPending(d: Pick<MemoDelivery, "state" | "outcome" | "defer">): boolean {
  return !!d.defer && (d.state !== "closed" || (d.outcome === "deferred" && !d.defer.result));
}

/** What became of a run's hand-back request: asked (open run), applied, or why it was dropped.
 *  null for a run with none. A closed run with no result was closed in a way that does not read one
 *  (ended, aborted, ...): the request did not apply. */
export function deferWords(d: Pick<MemoDelivery, "state" | "outcome" | "defer">): MsgKey | null {
  if (!d.defer) return null;
  if (d.state !== "closed") return "memo.d_defer_asked";
  if (deferPending(d)) return "memo.d_defer_settling";
  return DEFER_RESULT_KEYS[d.defer.result] ?? "memo.d_defer_not_applied";
}

/** Whether "return to queue" is offered on a run: closed, not a wake, not returned already, with
 *  notes. The Control Plane decides what can return; this only hides the button where nothing can. */
export function canReturnRun(d: Pick<MemoDelivery, "state" | "outcome" | "returnedAt" | "messageIds" | "returnBlocked" | "defer">): boolean {
  if (d.state !== "closed" || d.outcome === "wake" || d.outcome === "not_sent" || d.returnedAt || d.messageIds.length === 0) return false;
  // A hand-back that went through already put its notes back; only those another note's work item
  // kept with the run are left to return.
  return d.outcome !== "deferred" || d.defer?.result !== "applied" || d.returnBlocked.length > 0;
}

/** What a run was for: the work item keys its notes point at, and a one-line title (the start of the
 *  first note's text; a reference carries no cached title). Only notes still in the list count, so a
 *  run whose notes were cleared names nothing. */
export function runWorkItems(
  messages: readonly Pick<Memo, "id" | "body" | "reference">[],
  d: Pick<MemoDelivery, "messageIds">,
): { keys: string[]; title: string } {
  const keys: string[] = [];
  let title = "";
  for (const m of messages) {
    if (!d.messageIds.includes(m.id)) continue;
    const k = m.reference && typeof m.reference.key === "string" ? m.reference.key : "";
    if (k && !keys.includes(k)) keys.push(k);
    if (!title) title = (m.body ?? "").trim().split("\n")[0].slice(0, 120);
  }
  return { keys, title };
}

/** "waiting until <time>" for a note its run handed back, while that time is still ahead. */
export function deferredUntil(m: Pick<Memo, "state" | "notBefore">, now: number = Date.now()): string | null {
  if (m.state !== "pending" || !m.notBefore) return null;
  const t = Date.parse(m.notBefore);
  return Number.isNaN(t) || t <= now ? null : m.notBefore;
}

/** The pull requests of a run's session that are open or merged, as "#12" / "owner/repo#12", from
 *  the session list the Console holds. Empty when the session is not listed (archived, or the list
 *  is not loaded): the Control Plane does not refuse on it, so this is a warning and nothing more. */
export function runOpenPRs(session: Pick<Session, "pr" | "links"> | undefined): string[] {
  if (!session) return [];
  const out: string[] = [];
  if (session.pr && (session.pr.state === "open" || session.pr.state === "merged")) out.push("#" + session.pr.number);
  for (const l of session.links ?? []) {
    // The links the run's own work holds, as the Control Plane's PR snapshot reads them: not a PR it
    // only reviewed or watched.
    if (l.role !== "started" && l.role !== "created" && l.role !== "working") continue;
    if ((l.state === "open" || l.state === "draft" || l.state === "merged") && l.key && !out.some((k) => l.key.endsWith(k))) out.push(l.key);
  }
  return out;
}

// ---- a start time a note carries (ADR 0137, addendum 2026-11-22) ------------------------------

/** The start time of a note of a queue while it is still ahead, else null. A held note shows it too:
 *  approving the note approves the time. */
export function scheduledUntil(m: Pick<Memo, "state" | "startAfter">, now: number = Date.now()): string | null {
  if ((m.state !== "pending" && m.state !== "held") || !m.startAfter) return null;
  const t = Date.parse(m.startAfter);
  return Number.isNaN(t) || t <= now ? null : m.startAfter;
}

const pad2 = (n: number) => String(n).padStart(2, "0");

const wallFormats = new Map<string, Intl.DateTimeFormat>();
/** The wall time of an instant in zone, as "YYYY-MM-DDTHH:MM" with Latin digits; "" for no date. The
 *  zone is explicit: the editor reads and writes in the zone it opened with, whatever the setting
 *  becomes while it is open. */
function wallIn(ms: number, zone: string): string {
  if (Number.isNaN(ms)) return "";
  let f = wallFormats.get(zone);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      numberingSystem: "latn",
      calendar: "gregory",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
      hourCycle: "h23",
    });
    wallFormats.set(zone, f);
  }
  const p: Record<string, number> = {};
  for (const x of f.formatToParts(ms)) if (x.type !== "literal") p[x.type] = parseInt(x.value, 10);
  return `${String(p.year).padStart(4, "0")}-${pad2(p.month)}-${pad2(p.day)}T${pad2(p.hour % 24)}:${pad2(p.minute)}`;
}

/** The offset of zone at an instant, in minutes east of UTC. */
function offsetIn(ms: number, zone: string): number {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(wallIn(ms, zone));
  if (!m) return 0;
  return Math.round((Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]) - Math.floor(ms / 60000) * 60000) / 60000);
}

/** A stored start time as a datetime-local input's value ("YYYY-MM-DDTHH:MM") in zone (the member's
 *  effective one by default, not the browser's). "" for none. */
export function startAfterInputValue(iso: string | undefined, zone: string = effectiveTimeZone()): string {
  return iso ? wallIn(Date.parse(iso), zone) : "";
}

/** A datetime-local value, read as a wall time of zone (the member's effective one by default), as
 *  the RFC 3339 time (UTC) the Control Plane takes. "" for an empty value (no start time); null for
 *  one that is not a time of that zone: unreadable, or skipped by a DST change (02:30 on the morning
 *  the clocks go forward). A wall time a DST change repeats is read as its later instant: the note
 *  waits for the second 01:30, never runs before what the member may have meant. */
export function startAfterFromInput(local: string, zone: string = effectiveTimeZone()): string | null {
  if (local === "") return "";
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local);
  if (!m) return null;
  const wall = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
  // The offsets a day either side cover both sides of any change near this wall time; a candidate
  // counts only when it reads back as the same wall time.
  const day = 86400000;
  const fits = [wall - day, wall, wall + day]
    .map((at) => wall - offsetIn(at, zone) * 60000)
    .filter((t) => wallIn(t, zone) === local);
  return fits.length === 0 ? null : new Date(Math.max(...fits)).toISOString();
}

/** A start time as the row and the approval card show it: with the zone's short name, so the two
 *  instants of a wall time a DST change repeats read differently. */
export function fmtStartAfter(iso: string): string {
  return fmtDateTime(iso, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZoneName: "short" });
}
