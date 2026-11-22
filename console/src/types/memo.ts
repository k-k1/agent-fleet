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

/** A pointer to a work item (an issue, a ticket, a pull request). Never a copy of it: the item's
 *  text stays with the tracker and the session reads it there. The Control Plane stores the key
 *  normalised (lower case for github and bitbucket, upper case for jira). */
export interface MemoReference {
  provider: string;
  key: string;
  url: string;
}

export interface Memo {
  id: string;
  repo: string; // "repos/<repo>"-derived name; "" = common bucket
  category: string; // free-form sub-project label; "" = unfiled
  kind: MemoKind;
  body: string; // free text, or a comment when kind=file
  refPath: string; // kind=file: "~/repos/<repo>/..."
  attachments?: MemoAttachment[]; // image attachments (any kind); omitted = none
  /** The work item this note points at; absent on a note that has none. A note with one may have
   *  an empty body. */
  reference?: MemoReference;
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
  /** "setting" when the category's accept-from-own-sessions switch made the message pending and no
   *  approval did; "tidy" when a tidy run put it there; "continuation" when it was admitted as a
   *  continuation of an approved note (ADR 0137, addendum 2026-11-20). */
  releasedBy?: string;
  /** Set on a continuation: the first note of its chain, and its place in it. */
  chainId?: string;
  chainSeq?: number;
  /** The delivery (one fire) that took the message (#4163). */
  deliveryId?: string;
  /** Set on a message a tidy run made from other notes: their ids. Absent on any other. */
  derivedFrom?: string[];
  /** The provenance of the notes a derived message was made from (member, workspace or mcp). */
  sourceProvenance?: string;
  /** The tidy run that made, moved or replaced the message. */
  tidyRunId?: string;
  /** The category a tidy run moved the message from. repo and name are empty when the Control
   *  Plane could not name it: a single create or update answer, or a category since deleted. */
  tidyFrom?: { id: string; repo: string; name: string };
  /** The notes of the same auto-run category this one waits for, by id (ADR 0137, addendum
   *  2026-11-19). Absent for none. */
  after?: string[];
  /** How each of them stands, on a waiting or held note of the list only. */
  afterStatus?: MemoAfterItem[];
  /** Set on a note the Control Plane sent back for approval because one of the notes it waits for
   *  can no longer finish well. */
  heldReason?: { after: string; reason: string; at?: string };
  /** A note its run handed back (ADR 0137, addendum 2026-11-19, part B): when it may run again,
   *  how many times it was handed back since it was last approved or returned, and those hand-backs
   *  with the sessions' reasons (the Console's list only). */
  notBefore?: string;
  /** The earliest time the note may run (ADR 0137, addendum 2026-11-22): set by whoever wrote it,
   *  part of what an approval covers, and kept by it. Absent for none. */
  startAfter?: string;
  deferCount?: number;
  deferReasons?: MemoDeferEntry[];
}

/** One hand-back of a note: the session's reason, when, and by which run. */
export interface MemoDeferEntry {
  reason: string;
  at: string;
  delivery?: string;
}

/** One note another waits for, and how it stands: satisfied, waiting (with why) or failed (with
 *  why). label is its work-item key, else the start of its text. */
export interface MemoAfterItem {
  id: string;
  label?: string;
  state: "satisfied" | "waiting" | "failed";
  reason?: string;
  /** The PRs of its run, as last read from the workspace, for reason "pr" (waiting for them to
   *  merge) and "pr_closed". Absent while none was read. */
  prs?: MemoAfterPR[];
}

/** One PR of a run a note waits for. key is "owner/repo#N"; state is open, draft, merged, closed,
 *  or "" when the provider could not show it. */
export interface MemoAfterPR {
  provider: string;
  key: string;
  state: string;
  url?: string;
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
  /** A work item to point at. At most one open note per item in an auto-run category: adding
   *  one that is already there answers the existing note (`existing: true`). */
  reference?: MemoReference;
  /** Notes of the same auto-run category this one waits for: ids, or work-item keys. */
  after?: string[];
  position?: number;
}

/** What POST /api/memos answers: the saved note, or the one already holding the same item. */
export type MemoCreated = Memo & { existing?: boolean; error?: { code?: string; message?: string } };

// Partial edit sent to PATCH /api/memos/{id}; omitted fields are left unchanged.
export interface MemoPatch {
  repo?: string;
  category?: string;
  body?: string;
  refPath?: string;
  attachments?: MemoAttachment[];
  /** Three meanings, told apart by the key alone: absent leaves the reference as it is, `{}`
   *  clears it, a full object sets it. Never send `null`: the Control Plane reads it as absent. */
  reference?: MemoReference | Record<string, never>;
  /** Absent leaves the notes it waits for as they are, a list replaces them, [] clears them. */
  after?: string[];
  /** Absent leaves the start time as it is, an RFC 3339 time with an offset sets it, "" clears it. */
  startAfter?: string;
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
  /** Notes from sessions the member started themselves start without an approval. */
  acceptOwnSessions: boolean;
  /** How many follow-ups of one approved note a run of this category may add without another
   *  approval (0 turns it off; the Control Plane allows at most 10). */
  continuationMax: number;
  /** A model in the workspace tidies the waiting notes before an automatic run. */
  tidy: boolean;
  /** The auto-run categories a tidy may move a message to so that it waits to run there. */
  tidyDestinations: string[];
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
  /** Set while the notes that point at an item are held back because the workspace's Agent
   *  cannot take them yet; notes without one still run. */
  noRefsAt: string;
  /** Set while the notes a tidy made are held back because the workspace's Agent cannot take them. */
  noTidyAt: string;
  enabled: boolean;
  generation: number;
  createdAt: string;
  updatedAt: string;
  /** Why notes are waiting and nothing starts now (read-only, the list only). Absent when a run
   *  can start, when nothing waits, and from a Control Plane that does not send it. */
  waiting?: MemoQueueWaiting;
  /** Why a press of "Run now" would be refused now (read-only, the list only): nothing_waiting,
   *  prerequisites, deferred or no_place. Absent when a press would start a run, and from a
   *  Control Plane that does not send it; the claim stays the authority. */
  fireBlock?: MemoQueueWaiting;
}

/** The reasons the Control Plane gives, in the order it checks them (the first that holds). */
export type MemoWaitingReason =
  | "tenant_off"
  | "disabled"
  | "no_report"
  | "no_references"
  | "paused"
  | "window_closed"
  | "below_threshold"
  | "quiet_minute"
  | "daily_cap"
  | "backoff"
  | "usage_limit"
  | "no_place"
  | "tidying"
  | "no_tidy"
  | "prerequisites"
  | "deferred"
  | "scheduled"
  /** Only a fireBlock: no note waits to run. */
  | "nothing_waiting";

export interface MemoQueueWaiting {
  reason: MemoWaitingReason;
  /** RFC 3339: when the Control Plane will act, only when it is known. */
  until?: string;
  detail?: { needed?: number; waiting?: number; agentKind?: string; by?: "category" | "member"; blocked?: number; deferred?: number; scheduled?: number };
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
  | "acceptOwnSessions"
  | "continuationMax"
  | "tidy"
  | "tidyDestinations"
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
  outcome: string; // clean | deferred | failed | aborted | ended | unknown | not_sent ("" while not closed)
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
  /** The watches and timers behind a "wakers" hold (id and source only). Empty from an older
   *  Agent or Control Plane, and for an old row. */
  heldWakers: { id: string; source: string; timer: boolean; builtin: boolean }[];
  overdue: boolean;
  overdueAt: string;
  /** Started by the Control Plane itself rather than by "Run now". */
  auto: boolean;
  /** Where wrapping up the session stands; "released" = a person took the session over. */
  retire: string;
  /** How the first turn of the session ended; null until it did. */
  firstTurn: { outcome: string; ms: number; endedAt: string } | null;
  /** The hand-back the run's session asked for, null for none. result is what the close made of
   *  it: "applied", or why it was dropped ("turn", "pr", "unread", "released": a person took the session
   *  over; "returned": a person returned the notes first); "" before the close, and on a run closed as
   *  "deferred" until its session is stopped and the notes go back. */
  defer: { reason: string; notBefore: string; at: string; result: string } | null;
  /** The notes that stayed with the run because another waiting note holds their work item, and
   *  that note. */
  returnBlocked: { id: string; by: string }[];
  /** When a person returned the run's notes to the queue; "" for never. */
  returnedAt: string;
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

/** One run of the tidy before a fire (GET /api/memo-queues/{categoryId}/tidy-runs). */
export interface MemoTidyRun {
  id: string;
  trigger: string; // auto | manual
  state: string; // running | applied | kept | dropped | skipped | failed | undone
  /** A short code for how a run that changed nothing ended; "" otherwise. */
  reason: string;
  createdAt: string;
  finishedAt: string;
  undoneAt: string;
  merged: number;
  split: number;
  moved: number;
  /** Of the moved messages, those that now wait for approval. */
  held: number;
  sourceIds: string[];
  resultIds: string[];
  movedIds: string[];
  undoable: boolean;
  blocked: MemoTidyBlocked[];
}

/** A message that keeps a run from being undone; why is claimed, sent, changed, gone, later_run or duplicate_ref. */
export interface MemoTidyBlocked {
  id: string;
  why: string;
}

export interface MemoTidyRunsWire {
  runs: MemoTidyRun[];
  /** Runs reserved in the last 24 hours, of dailyCap. */
  usedToday: number;
  dailyCap: number;
}
