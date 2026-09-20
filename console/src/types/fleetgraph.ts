// Fleet session graph (ADR 0096 / docs/log/101) — FROZEN CONTRACT.
//
// Lanes are sessions, the horizontal axis is time. This file is types-only and
// import-only: it is the single seam the implementation lanes build against
// without consulting each other, and it is the authority on the wire's shape.
// Who owns which file is docs/log/101 §101.6.
//
// Two rules hold this together, and both exist because breaking them is silent:
//   - Time is unix MILLIS everywhere in this file. The ledger's own jsonl lines
//     are RFC3339; converting is the server's job, never the view's.
//   - "Nobody observed it" is a value (`unknown`), not a missing one. Anything
//     that folds it into "idle" reports work that never happened.

import type { Session, SessionKind } from "./session.ts";

// ── Identities ───────────────────────────────────────────────────────────────

// A lane is a session, always addressed by Session.name — the immutable random
// slug, never reused, so a rename, a handoff or a fork cannot break a ledger
// line's link. What is DRAWN is the display name; the slug is never shown to a
// human on its own.
export type LaneId = string;

// One end of an arrow. Only a LaneId becomes a lane; the rest have no birth and
// no death, so they are drawn as arrows leaving the figure's top/bottom edge
// rather than as horizontal bands (ADR 0096 decision 8-2):
//
//   "conv:<id>"        a chat conversation (the fleet operator's af_write conversation)
//   "user"             a person — the Console composer or the terminal's keyboard
//   "schedule"         scheduled execution (ADR 0021)
//   "bridge:discord"   the chat bridge, per platform
//   "bridge:slack"
//   "agent"            AF itself — the auto-resume nudge after a retryable cut-off,
//                      which is nobody's instruction and the one arrival that must
//                      never be attributed to the user (docs/log/47 §4-6)
//
// An unknown spelling renders as an anonymous external actor rather than being
// dropped: losing the arrow hides that the session was steered at all.
export type ActorId = LaneId | string;

// Meta.Origin's frozen vocabulary (ADR 0029 §6 + ADR 0073). "unknown" is a
// session older than the field — never fold it into "user", which would overstate
// what people opened themselves.
export type GraphOrigin = "user" | "operator" | "schedule" | "handoff" | "session" | "unknown";

// The state vocabulary the ledger records. It is deliberately NOT SessionState:
// that union is the Console's live-row axis ("" = idle) and is missing the states
// the agents emit around a stopped turn (agents/notify.go). A `string` here would
// type-check anything and let the writer and the reader disagree in silence.
//
// The writer NORMALISES: SessionState's "" is written as "idle", and a spelling
// outside this union is written as "idle" too — the reader never guesses.
export type LedgerState =
  | "working"
  | "idle"
  | "question"
  | "plan"
  | "permission"
  | "blocked" // waiting on a choice in the pane
  | "auth" // the login expired
  | "limited" // waiting for a usage limit to lift (may auto-resume)
  | "spend_limit" // a cap that waiting never clears
  | "failed"
  | "aborted";

// ── Ledger events (the REST DTO's payload) ───────────────────────────────────
// Lineage lines (fleet-graph/lineage.jsonl) are permanent: they survive the
// 7-day stopped-session prune that erases Meta, and die only when a person
// deletes the session (ADR 0096 decision 6). They carry no prompt text and no
// report text — what must disappear on deletion is never stored.

export interface BirthEvent {
  ev: "birth";
  ts: number;
  name: LaneId;
  kind: SessionKind;
  repo?: string;
  origin: GraphOrigin;
  originSession?: LaneId; // the parent that spawned it, or the session whose handoff a person launched
  // This session's own conversation id in its kind's id space, which is what
  // resolves another lane's `forkFrom` — a conversation id, not a session name —
  // back onto this lane.
  //
  // It must be resolved the SAME way the kind's own Forker.ForkSource resolves it
  // (claude = LiveSID, codex = the hook-captured per-slot id, opencode = the
  // current conversation in its store). Those are observed values, not the id AF
  // passed at launch: burning in the launch id makes codex and opencode fork edges
  // miss permanently. Absent when the kind cannot resolve one yet — a ConvIdEvent
  // fills it in later.
  conv?: string;
  forkFrom?: string; // Meta.ForkFrom — match against another lane's conv
  display?: string; // display name at birth (title → claude label → repo@MMDD-HHMM)
}

// The session's conversation id changed, or became resolvable for the first time.
// It changes mid-life on its own: when claude relaunches itself, --session-id
// structurally drops out of the argv and it starts writing under a new random id
// (internal/agents/claude/sid.go tracks exactly this). `conv` is therefore read as
// "the id in force at that time", never as a constant.
export interface ConvIdEvent {
  ev: "convid";
  ts: number;
  name: LaneId;
  conv: string;
}

// The end of one RUN, as observed. Meta.StoppedAt is filled lazily — at the first
// list that finds the slot gone — so this is when the end was first seen, not when
// it happened, and the view says so in the ×'s tooltip.
export interface DeathEvent {
  ev: "death";
  ts: number;
  name: LaneId;
  reason?: "oom" | "killed" | "crashed" | string; // absent = a clean/deliberate stop
  code?: number; // raw pane wait status (137 = OOM SIGKILL)
  signal?: number;
}

// A stopped session was resumed: the SAME lane starts another run. This event is
// not a convenience — a resume clears Meta.StoppedAt (sessionx/session_handlers.go,
// session_tmux.go, session_driver.go), so one lane's life is a sequence of runs and
// a model with a single death cannot say which × belongs to which stretch.
export interface ReviveEvent {
  ev: "revive";
  ts: number;
  name: LaneId;
}

// Archived (folded away, restorable) or restored. `archived:false` is the restore.
export interface ArchivedEvent {
  ev: "archived";
  ts: number;
  name: LaneId;
  archived: boolean;
}

export type LineageEvent = BirthEvent | ConvIdEvent | DeathEvent | ReviveEvent | ArchivedEvent;

// Activity lines (fleet-graph/activity-<YYYY-MM-DD>.jsonl, the date in UTC so a
// reader picking files out of a millis window cannot be thrown by the workspace's
// timezone) rotate — 30 days by default. Everything below exists only from the
// feature's installation forward.

// A live-state transition, written where the session list already derives state
// for every session: no new polling and no transcript scan (ADR 0096 decision 3).
//
// The writer holds the last state per session in process and writes ONLY on a
// change, so the two observers that drive the list (a Console at 4s, the control
// plane's reaper at 1m) cannot double-write. `from` comes from that map, and is
// absent when the map has no entry — after a restart, or for a session first seen
// mid-life. Absent `from` means "unknown before this", not "idle before this".
export interface StateEvent {
  ev: "state";
  ts: number;
  name: LaneId;
  from?: LedgerState;
  to: LedgerState;
}

// Every session's current state, written once right after an Agent restart (it
// also re-seeds the writer's last-state map). It marks the boundary of an
// observation gap: the stretch before it stays UNKNOWN rather than being
// back-filled with a guess.
export interface ResyncEvent {
  ev: "resync";
  ts: number;
  name: LaneId;
  to: LedgerState;
}

// An instruction that arrived from outside the lane. `source` uses the spellings
// transcript.Turn.Source already carries (sessionx/session_injections.go):
// operator | schedule | schedule-manual | discord | slack | spawn | auto-resume.
// `excerpt` is ≤140 chars, single line, DISPLAY-ONLY: never executed, sanitized at
// render (docs/30's prompt-injection stance).
export interface InstructEvent {
  ev: "instruct";
  ts: number;
  from: ActorId;
  to: LaneId;
  source?: string;
  excerpt?: string;
}

// A report leaving a session for the conversation that is owed one.
export interface ReportEvent {
  ev: "report";
  ts: number;
  from: LaneId;
  to: ActorId;
  kind?: "answer-ready" | "exit" | string;
  reason?: string; // oom | crashed | killed — the view draws these red
}

// A session-to-session message (ADR 0041). It is here and NOT in instr-ledger by
// construction: a peer send must never touch the arm (0041 decision 4).
export interface PeerEvent {
  ev: "peer";
  ts: number;
  from: LaneId;
  to: LaneId;
  intent: "request" | "question" | "answer" | "notice" | string;
  excerpt?: string;
}

export type ActivityEvent = StateEvent | ResyncEvent | InstructEvent | ReportEvent | PeerEvent;

// ── REST DTO ─────────────────────────────────────────────────────────────────

// How far back each layer actually reaches. The view draws these boundaries
// rather than letting the figure fade out silently: "no arrows were kept here"
// and "nothing happened here" look identical otherwise (ADR 0096 decision 8).
export interface GraphCoverage {
  activitySince: number | null; // oldest activity line available (null = none kept)
  lineageSince: number | null; // oldest lineage line available
  // Lineage older than this was back-filled from Meta at first start: it has one
  // birth and at most one death per lane, never arrows, bands or earlier runs.
  backfilledBefore?: number;
}

// Body of GET /api/fleet-graph?since=<ms>&until=<ms>.
export interface FleetGraphPage {
  since: number; // window the server actually served (it may narrow the request)
  until: number;
  now: number; // the Agent's clock, so "now" is not the browser's
  // NOT simply "the lineage events inside the window". The server always adds:
  //   1. every lineage event within [since, until];
  //   2. the birth (and the newest preceding convid) of every lane that overlaps
  //      the window, however old — without it a lane born three days before a
  //      24-hour window has no kind, no origin and no label;
  //   3. the birth of those lanes' ancestors, for family ordering (decision 9).
  // An ancestor that does not itself overlap the window is context for ordering
  // and labels only; the builder gives it no lane.
  lineage: LineageEvent[];
  activity: ActivityEvent[];
  coverage: GraphCoverage;
}

// ── Render model (BuildFleetGraph's output) ──────────────────────────────────

// Whether the lane's subject still exists, which is what the line style says
// (ADR 0096 decision 12): dashed = still there (resumable), ending = gone.
//   live     — running now: solid line + activity band
//   stopped  — still listed and resumable: dashed to the right edge
//   archived — folded away, restorable: faintly dashed
//   gone     — pruned or deleted: the line ENDS at its last ×
export type LanePresence = "live" | "stopped" | "archived" | "gone";

// One run of a lane: birth or revive → death, or still open. A lane has as many
// of these as it has been resumed.
export interface LaneRun {
  t0: number;
  t1: number | null; // null = no death observed for this run
  exitReason?: string;
  exitCode?: number;
}

export interface GraphLane {
  id: LaneId;
  row: number; // 0-based row index, in family order (a parent, then its children)
  depth: number; // 0 = a root; children nest under their parent
  parent?: LaneId; // originSession, when that lane is present in this window
  label: string; // display name
  kind: SessionKind;
  origin: GraphOrigin;
  runs: LaneRun[]; // chronological, never empty
  presence: LanePresence;
  state?: LedgerState; // live state right now (live lanes)
}

// A stretch of one lane. "unknown" is the honest default: nobody observed the
// session during it (no Console open, the reaper off, an Agent restart), and it
// must NOT be drawn as idle — "no evidence" is not "idle" (docs/log/51).
export type SegmentKind = "active" | "idle" | "waiting" | "unknown" | "stopped" | "archived";

// The coarse band a recorded state paints. S-LOGIC owns the table; the shape is
// frozen here so the view cannot invent a second one:
//   working                                   → active
//   idle | failed | aborted                   → idle
//   question | plan | permission | blocked
//     | auth | limited | spend_limit          → waiting
// The exact word survives on GraphSegment.state — the band is for colour, the
// state is for the tooltip, and that is why "limited" need not be its own band.
export type SegmentKindByState = Record<LedgerState, SegmentKind>;

export interface GraphSegment {
  laneId: LaneId; // a lane's id, NOT its row index
  t0: number;
  t1: number; // the window's end for an open segment
  kind: SegmentKind;
  state?: LedgerState; // the observed state this segment was derived from
}

// spawn / fork / handoff connect two lanes at the child's birth; instruct /
// report / peer are the round trips. A `fromRow` / `toRow` of null means that end
// is an external actor, so the arrow leaves the figure (decision 8-2).
export type ArrowVariant = "spawn" | "fork" | "handoff" | "instruct" | "report" | "peer";

export interface GraphArrow {
  ts: number;
  variant: ArrowVariant;
  from: ActorId;
  to: ActorId;
  fromRow: number | null;
  toRow: number | null;
  x: number; // laid-out horizontal px
  label?: string; // excerpt / caption
  danger?: boolean; // an exit report with a reason — render red
}

// Where a layer of history stops, drawn as a vertical boundary with a caption.
export interface CoverageMark {
  t: number;
  kind: "activity-start" | "lineage-start" | "backfill-start";
  x: number;
}

// The window and the geometry the model was laid out for. It is what the helpers
// below take, so a caller can map a time or a row without carrying the model.
export interface GraphScale {
  from: number;
  to: number;
  width: number;
  laneH: number;
}

// Plain data only — no methods. The view renders a fixture straight from JSON and
// a test compares two models, both of which a function member would break (the
// same reason lib/gitgraph.ts returns rows and exports its helpers separately).
export interface GraphModel {
  scale: GraphScale;
  lanes: GraphLane[];
  segments: GraphSegment[];
  arrows: GraphArrow[];
  marks: CoverageMark[];
  height: number;
}

// Exported alongside BuildFleetGraph by S-LOGIC. Linear, so the gap between two
// events is readable as time (ADR 0096 decision 8).
export type GraphXOf = (scale: GraphScale, ts: number) => number;
export type GraphLaneY = (scale: GraphScale, row: number) => number;

export interface BuildFleetGraphOptions {
  from?: number; // window start (default: to − 24h)
  to?: number; // window end (default: page.now)
  width?: number; // drawable width in px
  laneH?: number; // vertical px per lane
  showArchived?: boolean; // default true; the toggle lives in PaneContent, not React state
  // Keep only the lanes this conversation touched — how ADR 0027's "one operator
  // conversation's round trips" is absorbed into this figure.
  conversationId?: string;
}

// Implemented by S-LOGIC in console/src/lib/fleetgraph.ts. It merges the served
// page with the live sessions map (name → Session): the page is history, the map
// is what is true right now, and `presence` needs both — a lane whose newest run
// ended is "stopped" while the session is still listed, and "gone" once it is not.
// `page` may be null while the first load is in flight.
export type BuildFleetGraph = (
  page: FleetGraphPage | null,
  sessions: Map<string, Session>,
  opts?: BuildFleetGraphOptions,
) => GraphModel;
