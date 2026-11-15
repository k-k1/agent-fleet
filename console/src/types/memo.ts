// Memo queue domain types (docs/log/21). A Memo is a note queued per membership and
// synced across devices, then flushed to a coding session as one concatenated
// message. Notes are grouped by repo then a free-form category (sub-project);
// repo="" is the common/unfiled bucket. Persisted in the Control Plane SQLite.

export type MemoKind = "file" | "text";

// An image attached to a memo (docs/log/21 image attachments). path is the absolute in-container path
// returned by memoPasteImage (under ~/.cache/agent-fleet/memo-images); name is the
// basename, used both for display and to preview via GET api/memos/images/{name}.
export interface MemoAttachment {
  path: string;
  name: string;
}

export interface Memo {
  id: string;
  repo: string; // "repos/<repo>"-derived name; "" = common bucket
  category: string; // free-form sub-project label; "" = unfiled
  kind: MemoKind;
  body: string; // free text, or a comment when kind=file
  refPath: string; // kind=file: "~/repos/<repo>/..."
  attachments?: MemoAttachment[]; // image attachments (any kind); omitted = none
  position: number; // order within its group
  createdAt: string; // RFC3339
  sentAt: string; // "" = unsent; RFC3339 stamp = flushed, kept until retention sweep
  /** Set by the Control Plane on a memo taken in from the member inbox (ADR 0111): the item
   *  and its sender. Absent on every memo the member or their agent wrote. */
  origin?: { itemId: string; sender: string };
  /** Message-queue fields (ADR 0137), all written by the Control Plane. state is "" (or absent)
   *  on a plain note. A client never sends them back. */
  state?: MemoState;
  /** Which face created the message: "member" (Console), "workspace" (a session), "mcp", "inbox". */
  provenance?: string;
  /** Attribution only: the session name (workspace) or connection id (mcp). */
  producer?: string;
  producerOrigin?: string;
  /** What a release names: the hash of the content the card showed. */
  contentHash?: string;
  releasedAt?: string;
  /** The delivery (one fire) that took the message (#4163). */
  deliveryId?: string;
}

/** A message's state in a queue; "" is a note outside any queue. */
export type MemoState = "" | "held" | "pending" | "claimed" | "delivered" | "unconfirmed" | "superseded";

// The user-editable subset sent to POST /api/memos.
export interface MemoInput {
  repo?: string;
  category?: string;
  kind: MemoKind;
  body?: string;
  refPath?: string;
  attachments?: MemoAttachment[];
  position?: number;
}

// Partial edit sent to PATCH /api/memos/{id}; omitted fields are left unchanged.
export interface MemoPatch {
  repo?: string;
  category?: string;
  body?: string;
  refPath?: string;
  attachments?: MemoAttachment[];
  position?: number;
}

// A first-class category (docs/log/21 UI overhaul): created ahead of any memo, reordered by drag,
// kept while empty. name is unique within a (repo) bucket and stays the grouping key that
// Memo.category references — so a rename cascades onto the memos.
export interface MemoCategory {
  id: string;
  repo: string; // "" = common bucket, mirrors Memo.repo
  name: string;
  position: number; // order within the repo bucket
  createdAt: string; // RFC3339
  /** True when the category has auto-run settings (a row in memo_queue), enabled or not. */
  queue?: boolean;
}

/** Auto-run settings of a category (GET /api/memo-queues, PUT /api/memo-queues/{categoryId}). */
export interface MemoQueue {
  categoryId: string;
  repo: string;
  category: string;
  instruction: string;
  agentKind: string;
  model: string;
  effort: string;
  route: string;
  spendCapUsd: number;
  worktree: boolean;
  maxActive: number;
  batch: number;
  /** Hours after which a run that is still open counts as overdue. */
  maxTaskHours: number;
  /** When it runs by itself: openCron + openMinutes + tz is a time window, openAfter a number of
   *  waiting notes, neither means any time. maxFiresPerDay bounds the runs of 24 hours. */
  openCron: string;
  openMinutes: number;
  tz: string;
  openAfter: number;
  maxFiresPerDay: number;
  /** Read-only state the Control Plane sets: never part of a PUT. */
  paused: boolean;
  pausedAt: string;
  pausedReason: string;
  draining: boolean;
  /** Set while its runs by itself are held back because the workspace's Agent is too old to report. */
  noReportAt: string;
  enabled: boolean;
  generation: number;
  createdAt: string;
  updatedAt: string;
}

/** The PUT body: a PUT replaces the whole row, so every setting is sent, and nothing read-only. */
export type MemoQueueInput = Pick<
  MemoQueue,
  | "instruction"
  | "agentKind"
  | "model"
  | "effort"
  | "route"
  | "spendCapUsd"
  | "worktree"
  | "maxActive"
  | "batch"
  | "maxTaskHours"
  | "openCron"
  | "openMinutes"
  | "tz"
  | "openAfter"
  | "maxFiresPerDay"
  | "enabled"
>;

/** GET /api/memo-queues. tenantEnabled false: the tenant's administrator has not allowed new queues. */
export interface MemoQueuesWire {
  tenantEnabled: boolean;
  queues: MemoQueue[];
}

/** One fire of a queue (GET /api/memo-queues/{categoryId}/deliveries, #4163). */
export interface MemoDelivery {
  id: string;
  state: string; // claimed | sending | open | closed
  outcome: string; // clean | failed | aborted | unknown | not_sent ("" while not closed)
  session: string; // the started session's name; "" until known
  messageIds: string[];
  createdAt: string;
  sentAt: string;
  closedAt: string;
  /** Why a run that is still open keeps its place, at the Agent's last report: machine, person,
   *  limit, wakers or unknown; "" when nothing holds it or no report was read. */
  heldBy: string;
  /** The session of the tree that holds it, and when that was read. */
  heldSession: string;
  heldSeenAt: string;
  overdue: boolean;
  overdueAt: string;
  /** Started by the Control Plane itself rather than by "Run now". */
  auto: boolean;
  /** Where wrapping up the session stands; "released" = a person took the session over. */
  retire: string;
  /** How the first turn of the session ended; null until it did. */
  firstTurn: { outcome: string; ms: number; endedAt: string } | null;
}

// Body sent to POST /api/memo-categories.
export interface MemoCategoryInput {
  repo?: string;
  name: string;
}

// Partial edit sent to PATCH /api/memo-categories/{id}; omitted fields are unchanged.
// A name change cascades onto the memos (and merges into a same-name category).
export interface MemoCategoryPatch {
  name?: string;
  position?: number;
}
