// Fleet session graph (ADR 0096 / docs/log/101) — FROZEN CONTRACT (Phase 0).
//
// Lanes are sessions, the horizontal axis is time. This file is types-only and
// import-only: it is the seam the three parallel implementation sessions build
// against independently, and it is the authority on the wire's shape.
//
//   - S-BE    writes the two ledgers and serves GET /api/fleet-graph as FleetGraphPage
//   - S-LOGIC implements BuildFleetGraph in console/src/lib/fleetgraph.ts
//   - S-VIEW  renders a GraphModel in features/fleetgraph/FleetGraphView.tsx
//
// Keep it body-free (no runtime code) so it compiles standalone, and keep the
// shared pane wiring out of P0 entirely (that lives wholly in S-VIEW).
//
// It replaces types/opgraph.ts, the only artefact of ADR 0027 (superseded): that
// figure was one operator conversation's vertical sequence diagram, and a
// conv-scoped model structurally cannot carry a peer message (ADR 0041 decision 9).

import type { Session, SessionKind, SessionState } from "./session.ts";

// ── Identities ───────────────────────────────────────────────────────────────

// A lane is a session, always addressed by Session.name — the immutable random
// slug, never reused (so a rename, a handoff or a fork cannot break a ledger
// line's link). What is DRAWN is the display name; the slug is never shown to a
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
//
// An unknown spelling is rendered as an anonymous external actor rather than
// dropped: losing the arrow hides that the session was steered at all.
export type ActorId = LaneId | string;

// Meta.Origin's frozen vocabulary (ADR 0029 §6 + ADR 0073). "unknown" is a
// session older than the field — never fold it into "user", which would overstate
// what people opened themselves.
export type GraphOrigin = "user" | "operator" | "schedule" | "handoff" | "session" | "unknown";

// ── Ledger events (the REST DTO's payload) ───────────────────────────────────
// Lineage lines (fleet-graph/lineage.jsonl) are permanent: they survive the
// 7-day stopped-session prune that erases Meta, and die only when a person
// deletes the session (ADR 0096 decision 6). They carry no prompt text and no
// report text — what must disappear on deletion is never stored.

export interface BirthEvent {
  ev: "birth";
  ts: number; // unix millis
  name: LaneId;
  kind: SessionKind;
  repo?: string;
  origin: GraphOrigin;
  originSession?: LaneId; // the parent that spawned it, or the session whose handoff a person launched
  // This session's OWN conversation id in its kind's id space (claude = sid,
  // opencode = ses_…, codex = uuid). It is what resolves `forkFrom` — which is a
  // conversation id, not a session name — back onto a lane.
  //
  // It is the id AF ASSIGNED, never an observed one: right after a fork claude
  // reads the SOURCE session's transcript until its own jsonl materialises, so an
  // observed value would put the parent's id on the child's line and the fork edge
  // would point at itself (ADR 0096 decision 13).
  conv?: string;
  forkFrom?: string; // source conversation id (Meta.ForkFrom) — match against another lane's conv
  display?: string; // display name at birth (title → claude label → repo@MMDD-HHMM)
}

// The session's conversation id changed mid-life. claude relaunching itself drops
// --session-id from its argv and starts writing under a new random id (measured on
// 2.1.239; internal/agents/claude/sid.go tracks exactly this), so `conv` has to be
// read as "the id in force at that time", not as a constant.
export interface ConvIdEvent {
  ev: "convid";
  ts: number;
  name: LaneId;
  conv: string;
}

// The end of a session, as OBSERVED. Meta.StoppedAt is filled lazily (the first
// list that sees the slot exited), so this is when the end was first seen, not
// when it happened — the view says so in the ×'s tooltip.
export interface DeathEvent {
  ev: "death";
  ts: number;
  name: LaneId;
  reason?: "oom" | "killed" | "crashed" | string; // absent = a clean/deliberate stop
  code?: number; // raw pane wait status (137 = OOM SIGKILL)
  signal?: number;
}

// Archived (folded away, restorable) or restored. `archived:false` is the restore.
export interface ArchivedEvent {
  ev: "archived";
  ts: number;
  name: LaneId;
  archived: boolean;
}

export type LineageEvent = BirthEvent | ConvIdEvent | DeathEvent | ArchivedEvent;

// Activity lines (fleet-graph/activity-<YYYY-MM-DD>.jsonl) rotate — 30 days by
// default. Everything here exists only from the feature's installation forward.

// A live-state transition, written where the session list already derives state
// for every session (no new polling, no transcript scan — ADR 0096 decision 3).
export interface StateEvent {
  ev: "state";
  ts: number;
  name: LaneId;
  from?: SessionState | string;
  to: SessionState | string;
}

// Every session's current state, written once right after an Agent restart, so
// the stretch across the restart stays UNKNOWN instead of being back-filled with
// a guess.
export interface ResyncEvent {
  ev: "resync";
  ts: number;
  name: LaneId;
  to: SessionState | string;
}

// An instruction that arrived from outside the lane (the operator, a person, the
// scheduler, a bridge). `excerpt` is ≤140 chars, single line, DISPLAY-ONLY: never
// executed, sanitized at render (docs/30's prompt-injection stance).
export interface InstructEvent {
  ev: "instruct";
  ts: number;
  from: ActorId;
  to: LaneId;
  source?: string; // transcript.Turn.Source spelling: operator | schedule | schedule-manual | discord | slack | spawn
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
// rather than letting the figure fade out silently: "no arrows here" and "nothing
// happened here" look identical otherwise (ADR 0096 decision 8).
export interface GraphCoverage {
  activitySince: number | null; // oldest activity line available (null = none kept)
  lineageSince: number | null; // oldest lineage line available
  // Lineage older than this was back-filled from Meta at first start, so it has
  // birth/death but no arrows and no bands, ever.
  backfilledBefore?: number;
}

// Body of GET /api/fleet-graph?since=<ms>&until=<ms>.
export interface FleetGraphPage {
  since: number; // window the server actually served (it may narrow the request)
  until: number;
  now: number; // the Agent's clock, so "now" is not the browser's
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
//   gone     — pruned or deleted: the line ENDS at the ×
export type LanePresence = "live" | "stopped" | "archived" | "gone";

export interface GraphLane {
  id: LaneId;
  lane: number; // row index, 0-based, in family order (parent, then its children)
  depth: number; // 0 = a root; children nest under their parent
  parent?: LaneId; // originSession, when that lane is present in this window
  label: string; // display name
  kind: SessionKind;
  origin: GraphOrigin;
  t0: number; // birth (or the window's start for a lane that predates it)
  t1: number | null; // death; null = no death observed
  presence: LanePresence;
  state?: SessionState | string; // live state right now (live lanes)
  exitReason?: string;
}

// A stretch of one lane. "unknown" is the honest default: nobody observed the
// session during it (no Console open, the reaper off, an Agent restart), and it
// must NOT be drawn as idle — "no evidence" is not "idle" (docs/log/51).
export type SegmentKind = "active" | "idle" | "waiting" | "unknown" | "stopped" | "archived";

export interface GraphSegment {
  lane: LaneId;
  t0: number;
  t1: number; // the window's end for an open segment
  kind: SegmentKind;
  state?: SessionState | string; // the observed state this segment was derived from
}

// spawn / fork / handoff connect two lanes at the child's birth; instruct /
// report / peer are the round trips. `from` or `to` being an ActorId that is not
// a lane means the arrow leaves the figure (decision 8-2).
export type ArrowVariant = "spawn" | "fork" | "handoff" | "instruct" | "report" | "peer";

export interface GraphArrow {
  ts: number;
  variant: ArrowVariant;
  from: ActorId;
  to: ActorId;
  fromLane: number | null; // null = an external actor (draw from the top edge)
  toLane: number | null;
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

export interface GraphModel {
  lanes: GraphLane[];
  segments: GraphSegment[];
  arrows: GraphArrow[];
  marks: CoverageMark[];
  from: number; // the rendered window
  to: number;
  width: number;
  height: number;
  laneH: number;
  xOf: (ts: number) => number; // time → px (linear; ADR 0096 decision 8)
  laneY: (lane: number) => number; // lane index → px
}

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

// Implemented by S-LOGIC in console/src/lib/fleetgraph.ts. Merges the served page
// with the live sessions map (name → Session): the page is history, the map is
// what is true right now, and `presence` needs both — a lane with a death event is
// "stopped" while the session is still listed and "gone" once it is not.
// `page` may be null while the first load is in flight.
export type BuildFleetGraph = (
  page: FleetGraphPage | null,
  sessions: Map<string, Session>,
  opts?: BuildFleetGraphOptions,
) => GraphModel;
