// Pure logic of the memo section's auto-run side (ADR 0137): reading what the Control Plane
// answers, the words for a message's state and origin, the release cards, and the settings
// form. No React and no module-level state, so nothing leaks between tests.
//
// Vocabulary: members never see the word "queue" (it already names a session's waiting inputs,
// ADR 0105). Every working term lives in one catalogue key (memo.state_*, memo.autorun*, ...),
// so the owner can change a word with one line.
import type { MsgKey } from "../../lib/i18n/index.ts";
import { browserTimeZone, isValidTimeZone } from "../../lib/intl.ts";
import type { Memo, MemoDelivery, MemoQueue, MemoQueueInput, MemoQueueWaiting, MemoQueuesWire, MemoWaitingReason } from "../../types/memo.ts";

// Limits the Control Plane enforces (control-plane/memo_queue.go). The form checks them first
// so a mistake is named next to its field; the Control Plane stays the authority.
export const QUEUE_MAX_ACTIVE_MAX = 6;
export const QUEUE_BATCH_MAX = 20;
export const QUEUE_INSTRUCTION_MAX_BYTES = 16 << 10;
export const QUEUE_SPEND_MAX = 10000;
export const QUEUE_TASK_HOURS_MAX = 72;
export const QUEUE_FIRES_PER_DAY_MAX = 200;
export const QUEUE_WINDOW_MAX_MINUTES = 24 * 60;
export const QUEUE_OPEN_AFTER_MAX = 100;
// What a row that never had these gets (the Control Plane's own defaults).
export const QUEUE_TASK_HOURS_DEFAULT = 12;
export const QUEUE_FIRES_PER_DAY_DEFAULT = 20;
export const QUEUE_WINDOW_DEFAULT_MINUTES = 60;

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown, d: number): number => (typeof v === "number" && Number.isFinite(v) ? v : d);

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
    },
  };
}

function readQueue(v: unknown): MemoQueue | null {
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
    enabled: v.enabled === true,
    generation: num(v.generation, 0),
    createdAt: str(v.createdAt),
    updatedAt: str(v.updatedAt),
    waiting: readWaiting(v.waiting),
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
  return { tenantEnabled: raw.tenantEnabled === true, queues };
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
      overdue: d.overdue === true,
      overdueAt: str(d.overdueAt),
      auto: d.auto === true,
      retire: str(d.retire),
      firstTurn: isObj(d.firstTurn) ? { outcome: str(d.firstTurn.outcome), ms: num(d.firstTurn.ms, 0), endedAt: str(d.firstTurn.endedAt) } : null,
    });
  }
  return out;
}

// ---- why nothing starts ---------------------------------------------------------------------

/** The line that says why a category with notes waiting starts nothing: a catalogue key and its
 *  parameters. Null when there is nothing to say: no `waiting` (a run can start, nothing waits, an
 *  older Control Plane), and for a pause and a workspace that has to be restarted (for runs, or for notes that point at an item), which have
 *  notices of their own that must not be said twice. `at` writes a time the way the screen does. */
export function waitingWords(
  q: Pick<MemoQueue, "waiting">,
  at: (iso: string) => string,
  agentName: (kind: string) => string,
): { key: MsgKey; params?: Record<string, string | number> } | null {
  const w = q.waiting;
  if (!w || w.reason === "paused" || w.reason === "no_report" || w.reason === "no_references") return null;
  const when = w.until && !Number.isNaN(Date.parse(w.until)) ? { at: at(w.until) } : null;
  switch (w.reason) {
    case "tenant_off":
      return { key: "memo.waiting_tenant_off" };
    case "disabled":
      return { key: "memo.waiting_disabled" };
    case "window_closed":
      return when ? { key: "memo.waiting_window_closed_at", params: when } : { key: "memo.waiting_window_closed" };
    case "below_threshold":
      return { key: "memo.waiting_below_threshold", params: { needed: w.detail?.needed ?? 0, count: w.detail?.waiting ?? 0 } };
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
  }
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
  | "maxTaskHours";
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
  if (!wholeIn(d.maxFiresPerDay, 1, QUEUE_FIRES_PER_DAY_MAX)) e.maxFiresPerDay = "memo.q_err_fires";
  if (!wholeIn(d.maxTaskHours, 1, QUEUE_TASK_HOURS_MAX)) e.maxTaskHours = "memo.q_err_task_hours";
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

const TURN_KEYS: Record<string, MsgKey> = {
  clean: "memo.d_turn_clean",
  "turn-failed": "memo.d_turn_failed",
  "turn-aborted": "memo.d_turn_aborted",
};

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
        case "failed":
          return "memo.d_state_failed";
        case "aborted":
          return "memo.d_state_aborted";
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
