// Engine indicator — wire types and pure read/derive helpers (ADR 0084 decisions 1/3/4/5/6/11).
//
// The member row is the admin row (`engine_admin.go` row()) with everything a member cannot
// act on cut away, the same way `engineTenantAdminRow` cuts a tenant_admin row down
// (decision 3). Every field here is OMITTED — never null/0/false — when the CP has no honest
// answer for it, so nothing on this side may invent a fallback (decision 4): an absent
// `stop_eta` means no countdown, not "unknown, so 0"; an absent role entirely means the pill
// does not render at all (decision 5 folds all four "do not show" conditions into one rule —
// "no row" — because the CP expresses every one of them by omitting the row).
//
// P0-A (the CP side that fills these fields and the `engines` push stream) lands separately
// from this file (the ADR's phase note: "B can be built stubbed against A's wire"). The shape
// below follows decision 3's field list verbatim (`key, api, state, warm, stop_eta,
// idle_secs, lifecycle, queue{...}`); `queue`'s own sub-fields are this file's own guess at a
// reasonable shape (see EngineQueue) since P0-A had not landed a JSON contract for it yet —
// called out again in the PR for reconciliation once P0-A merges.
//
// Kept free of React and of core/api/client.ts (which touches localStorage at module init),
// so it runs under the node vitest project like every other feature's read.ts.

export type EngineRole = "chat" | "images";

export interface EngineQueue {
  /** In-flight requests the CP gateway is holding for this row — decision 6-A, and (P1) 6-B
   *  for an image row. Always the SHARED count; nobody's own queue rides here in P0
   *  (decision 6's "自分の分" needs Agent's `/imagegen/queue`, which is P1). */
  count: number;
  /** Present only while the counter cannot yet vouch for `count` — a CP that replaced the
   *  previous process moments ago. Mirrors `window_counted_secs`'s honesty guard on the admin
   *  panel (decision 6 ⚠️, `engineUptime.ts`'s `windowIsPartial`). Absent once trustworthy. */
  counted_secs?: number;
}

export interface EngineMemberRow {
  key: string;
  api: EngineRole;
  /** `stopped | starting | running | stopping` — the same words `engineDisplayState()`
   *  returns on the admin panel (decision 4). Absent for an external/remote row: this
   *  deployment does not manage it and must not claim to know its state. */
  state?: string;
  warm?: boolean;
  /** RFC3339, absolute. The subtraction into "N minutes left" happens here, in the browser
   *  (decision 2) — never on the wire, or the `engines` stream's diff-only tick (decision 1)
   *  would carry a changing payload every 4 seconds even while nothing else moved. */
  stop_eta?: string;
  idle_secs?: number;
  /** `external | remote`. Absent = this deployment's own engine. */
  lifecycle?: "external" | "remote";
  queue?: EngineQueue;
}

export interface EnginesPayload {
  engines: EngineMemberRow[];
}

const str = (v: unknown): string | undefined => (typeof v === "string" && v ? v : undefined);
const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);

function normalizeQueue(raw: unknown): EngineQueue | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const r = raw as Record<string, unknown>;
  const count = num(r.count);
  if (count === undefined) return undefined;
  const counted_secs = num(r.counted_secs);
  return counted_secs === undefined ? { count } : { count, counted_secs };
}

function normalizeRow(raw: unknown): EngineMemberRow | null {
  if (!raw || typeof raw !== "object") return null;
  const r = raw as Record<string, unknown>;
  const key = str(r.key);
  const api = r.api === "chat" || r.api === "images" ? r.api : undefined;
  if (!key || !api) return null;
  const row: EngineMemberRow = { key, api };
  const state = str(r.state);
  if (state) row.state = state;
  if (r.warm === true) row.warm = true;
  const stopEta = str(r.stop_eta);
  if (stopEta) row.stop_eta = stopEta;
  const idle = num(r.idle_secs);
  if (idle !== undefined) row.idle_secs = idle;
  if (r.lifecycle === "external" || r.lifecycle === "remote") row.lifecycle = r.lifecycle;
  const queue = normalizeQueue(r.queue);
  if (queue) row.queue = queue;
  return row;
}

/** Adopt one `engines` push frame or `GET /api/engines/status` body. Garbage (a CP error
 *  envelope, a non-object, a missing `engines` array) becomes `null` so the caller can leave
 *  the previous rows on screen rather than blanking the pills on a transient hiccup — the
 *  same rule `readWorkItems` follows. */
export function readEngines(res: unknown): EngineMemberRow[] | null {
  if (!res || typeof res !== "object") return null;
  const d = res as { engines?: unknown; error?: unknown };
  if (d.error) return null;
  if (!Array.isArray(d.engines)) return null;
  const rows: EngineMemberRow[] = [];
  for (const raw of d.engines) {
    const row = normalizeRow(raw);
    if (row) rows.push(row);
  }
  return rows;
}

/** Fixed, not derived from what happens to be in the payload — a role with zero rows this
 *  tick must not reorder the surviving pill. */
export const ROLE_ORDER: EngineRole[] = ["chat", "images"];

export function groupByRole(rows: EngineMemberRow[]): Map<EngineRole, EngineMemberRow[]> {
  const g = new Map<EngineRole, EngineMemberRow[]>();
  for (const role of ROLE_ORDER) {
    const forRole = rows.filter((r) => r.api === role);
    if (forRole.length) g.set(role, forRole);
  }
  return g;
}

/** One row's rank for "the best state a role's rows are in" (decision 11) — higher wins.
 *  `lifecycle` is checked first: an external/remote row never gets to claim `warm` or a
 *  `state` word this deployment does not own, no matter what rides along on those fields
 *  (decision 4 — "ピルは『利用可』とだけ言う"). Among rows this deployment DOES manage, `warm`
 *  outranks everything: it is what actually decides how long the next request waits, which
 *  is the one fact this pill exists to report. */
function rowRank(r: EngineMemberRow): number {
  if (r.lifecycle) return 3;
  if (r.warm) return 5;
  switch (r.state) {
    case "running":
      return 4;
    case "starting":
      return 2;
    case "stopping":
      return 1;
    case "stopped":
      return 0;
    default:
      return -1;
  }
}

/** The row a role's pill takes its headline (state word + `stop_eta`) from: the best-ranked
 *  row among the role's rows (decision 11). Ties keep the first row in wire order, so the
 *  choice is stable across re-renders of an unchanged payload. */
export function pickHeadRow(rows: EngineMemberRow[]): EngineMemberRow {
  let best = rows[0];
  let bestRank = rowRank(best);
  for (const r of rows.slice(1)) {
    const rank = rowRank(r);
    if (rank > bestRank) {
      best = r;
      bestRank = rank;
    }
  }
  return best;
}

const QUEUE_GRACE_SECS = 5;

/** Whether a row's queue count can be trusted. A positive count is real either way — an
 *  in-flight counter can only ever undercount while it is young, never invent requests — so
 *  only a bare 0 next to a freshly-replaced counter is held back (decision 6 ⚠️, "確信のある
 *  0を出さない"). The grace window has no ADR-specified value (the field is new in P0-A); this
 *  picks a small margin, flagged in the PR for reconciliation once P0-A's actual shape lands. */
export function queueIsCertain(q: EngineQueue): boolean {
  if (q.count > 0) return true;
  return q.counted_secs === undefined || q.counted_secs >= QUEUE_GRACE_SECS;
}

/** The role's shared queue count: the SUM across every row, never just the head row's
 *  (decision 6 — "ピルが見出しに出す共有の数は、その役の行の合計"). `undefined` when no row
 *  carries a certain count, so the pill omits the line rather than asserting 0. */
export function totalQueue(rows: EngineMemberRow[]): number | undefined {
  let total: number | undefined;
  for (const r of rows) {
    if (!r.queue || !queueIsCertain(r.queue)) continue;
    total = (total ?? 0) + r.queue.count;
  }
  return total;
}

export type EngineStateWord = "ready" | "running" | "starting" | "stopping" | "stopped" | "available";

/** Which word describes a row, independent of i18n. `lifecycle` wins outright (decision 4:
 *  an external/remote row is never claimed to be running, starting or warm), then `warm`
 *  (decision 11), then the raw state. A row with neither reads as "available" too — absence
 *  of an opinion is not a license to guess one (decision 4). */
export function stateWord(row: EngineMemberRow): EngineStateWord {
  if (row.lifecycle) return "available";
  if (row.warm) return "ready";
  switch (row.state) {
    case "running":
      return "running";
    case "starting":
      return "starting";
    case "stopping":
      return "stopping";
    case "stopped":
      return "stopped";
    default:
      return "available";
  }
}

/** Whether the popover's cold-start hint belongs on this row — decision 5's "stopped は出す。
 *  これは『使えない』ではなく『最初の1回が遅い』であり、このインジケータが存在する主な理由
 *  そのもの". Self-managed and genuinely stopped only: an external/remote row's cold start is
 *  not this deployment's to describe, and `warm` already means the wait is over. */
export function showsColdHint(row: EngineMemberRow): boolean {
  return !row.lifecycle && row.state === "stopped" && !row.warm;
}

/** Seconds between now and an RFC3339 instant, positive when it is in the future. `null` for
 *  an absent or unparseable value — the caller renders that as "no line", never as 0
 *  (decision 4). A near-duplicate of the admin panel's `secsUntil`
 *  (`features/settings/admin/engineUptime.ts`), kept separate on purpose: that module sits
 *  behind the admin-only surface, and a pill every member's topbar mounts must not pull that
 *  bundle in to borrow one four-line function. */
export function secsUntil(at: string | undefined, now: number): number | null {
  if (!at) return null;
  const t = Date.parse(at);
  if (Number.isNaN(t)) return null;
  return Math.round((t - now) / 1000);
}

/** `secsUntil`, but only for a row this deployment manages. Decision 4's "ピルは『利用可』と
 *  だけ言う、カウントダウンの行は出さない" for an external/remote row is categorical — this
 *  holds even if a `stop_eta` rode along on the wire by mistake, rather than trusting the CP
 *  to have honored decision 4 on its own. */
export function stopSecs(row: EngineMemberRow, now: number): number | null {
  if (row.lifecycle) return null;
  return secsUntil(row.stop_eta, now);
}

/** Splits seconds into whole hours and minutes, rounding up to at least one minute once
 *  above zero — a 40-second wait that displayed as "0 minutes" reads as "it never moves".
 *  Same shape as the admin panel's `splitHM`, duplicated for the reason `secsUntil` is. */
export function splitHM(secs: number): { hours: number; mins: number } {
  if (secs <= 0) return { hours: 0, mins: 0 };
  const total = Math.max(1, Math.round(secs / 60));
  return { hours: Math.floor(total / 60), mins: total % 60 };
}
