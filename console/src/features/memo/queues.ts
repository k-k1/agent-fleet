// Pure logic of the memo section's auto-run side (ADR 0137): reading what the Control Plane
// answers, the words for a message's state and origin, the release cards, and the settings
// form. No React and no module-level state, so nothing leaks between tests.
//
// Vocabulary: members never see the word "queue" (it already names a session's waiting inputs,
// ADR 0105). Every working term lives in one catalogue key (memo.state_*, memo.autorun*, ...),
// so the owner can change a word with one line.
import type { MsgKey } from "../../lib/i18n/index.ts";
import type { Memo, MemoDelivery, MemoQueue, MemoQueueInput, MemoQueuesWire } from "../../types/memo.ts";

// Limits the Control Plane enforces (control-plane/memo_queue.go). The form checks them first
// so a mistake is named next to its field; the Control Plane stays the authority.
export const QUEUE_MAX_ACTIVE_MAX = 6;
export const QUEUE_BATCH_MAX = 20;
export const QUEUE_INSTRUCTION_MAX_BYTES = 16 << 10;
export const QUEUE_SPEND_MAX = 10000;

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown, d: number): number => (typeof v === "number" && Number.isFinite(v) ? v : d);

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
    maxActive: num(v.maxActive, 2),
    batch: num(v.batch, 1),
    enabled: v.enabled === true,
    generation: num(v.generation, 0),
    createdAt: str(v.createdAt),
    updatedAt: str(v.updatedAt),
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
    });
  }
  return out;
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

/** The form's state: numbers stay strings while typed, so "" and "1e" are representable. */
export interface QueueDraft {
  instruction: string;
  agentKind: string;
  model: string;
  effort: string;
  spendCapUsd: string;
  worktree: boolean;
  maxActive: string;
  batch: string;
  enabled: boolean;
}

export function draftOf(q: MemoQueue | undefined): QueueDraft {
  return {
    instruction: q?.instruction ?? "",
    agentKind: q?.agentKind || "claude",
    model: q?.model ?? "",
    effort: q?.effort ?? "",
    spendCapUsd: String(q?.spendCapUsd ?? 0),
    worktree: q?.worktree ?? true,
    maxActive: String(q?.maxActive ?? 2),
    batch: String(q?.batch ?? 1),
    enabled: q?.enabled ?? false,
  };
}

export type DraftField = "instruction" | "spendCapUsd" | "maxActive" | "batch";
export type DraftErrors = Partial<Record<DraftField, MsgKey>>;

const wholeIn = (s: string, lo: number, hi: number): boolean => {
  const t = s.trim();
  return /^\d+$/.test(t) && Number(t) >= lo && Number(t) <= hi;
};

/** What the form can say before the Control Plane does. One key per field. */
export function draftErrors(d: QueueDraft): DraftErrors {
  const e: DraftErrors = {};
  const bytes = new TextEncoder().encode(d.instruction.trim()).length;
  if (bytes > QUEUE_INSTRUCTION_MAX_BYTES) e.instruction = "memo.q_err_instruction_long";
  else if (d.enabled && bytes === 0) e.instruction = "memo.q_err_instruction";
  const spend = d.spendCapUsd.trim();
  if (!/^\d+(\.\d+)?$/.test(spend) || Number(spend) > QUEUE_SPEND_MAX) e.spendCapUsd = "memo.q_err_budget";
  if (!wholeIn(d.maxActive, 1, QUEUE_MAX_ACTIVE_MAX)) e.maxActive = "memo.q_err_max_active";
  if (!wholeIn(d.batch, 1, QUEUE_BATCH_MAX)) e.batch = "memo.q_err_batch";
  return e;
}

/** The PUT body. A PUT replaces the whole row, so the route, which this form does not edit, is
 *  sent back as it was or it would be blanked. Only call with no errors. */
export function draftBody(d: QueueDraft, route: string): MemoQueueInput {
  return {
    instruction: d.instruction.trim(),
    agentKind: d.agentKind.trim(),
    model: d.model.trim(),
    effort: d.effort.trim(),
    route,
    spendCapUsd: Number(d.spendCapUsd.trim()),
    worktree: d.worktree,
    maxActive: Number(d.maxActive.trim()),
    batch: Number(d.batch.trim()),
    enabled: d.enabled,
  };
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
