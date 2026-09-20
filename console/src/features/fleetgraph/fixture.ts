// Demo GraphModel — stands in for BuildFleetGraph (console/src/lib/fleetgraph.ts,
// owned by S-LOGIC and not yet landed: docs/log/101 §101.6 "P1 の担当境界"). It is
// deliberately NOT real data: every id, label and timestamp here is fixture-only, built
// fresh from the caller's window so the view always has something to draw during P1.
// P2 integration replaces every call site of buildFixtureGraph with
// BuildFleetGraph(page, sessions, opts) and deletes this file — the two return the exact
// same GraphModel shape (types/fleetgraph.ts), so the view itself needs no change.
//
// The fixture exists to exercise every branch the contract distinguishes, not to look
// like a plausible fleet: all four LanePresence values, a revived run, an erased lane,
// both flavors of "unknown" (never-observed vs. observed-but-unrecognised), all six
// ArrowVariant values including two that leave the figure's top edge, and a coverage
// boundary.
import type { GraphArrow, GraphLane, GraphModel, GraphScale, GraphSegment, CoverageMark } from "../../types/fleetgraph.ts";
import { xOf } from "./geometry.ts";

const H = 3_600_000;

export function buildFixtureGraph(scale: GraphScale): GraphModel {
  const now = scale.to;
  const t = (hoursAgo: number) => now - hoursAgo * H;

  const lanes: GraphLane[] = [
    {
      id: "fx-root1",
      row: 0,
      rootId: "fx-root1",
      depth: 0,
      presence: "live",
      erased: false,
      label: "fleet-graph kickoff",
      kind: "claude",
      origin: "user",
      // A revived lane: the first run ends cleanly, the second is still open — this is
      // what decision 12 calls out as the case a single-death model cannot represent.
      runs: [
        { t0: t(20), t1: t(16) },
        { t0: t(8), t1: null },
      ],
      state: "working",
    },
    {
      id: "fx-child1",
      row: 1,
      parent: "fx-root1",
      rootId: "fx-root1",
      depth: 1,
      presence: "stopped",
      erased: false,
      label: "S-VIEW lane",
      kind: "claude",
      origin: "session",
      runs: [{ t0: t(19), t1: t(3) }],
      // No `state`: never observed since the pane last polled — drawn as "not observed",
      // distinct from state === "unknown" (see the segment below).
    },
    {
      id: "fx-child2",
      row: 2,
      parent: "fx-child1",
      rootId: "fx-root1",
      depth: 2,
      presence: "archived",
      erased: false,
      label: "S-LOGIC lane",
      kind: "codex",
      origin: "session",
      runs: [{ t0: t(15), t1: t(6) }],
    },
    {
      id: "fx-probe1",
      row: 3,
      rootId: "fx-probe1",
      depth: 0,
      presence: "gone",
      erased: false,
      label: "throwaway GPU probe",
      kind: "shell",
      origin: "user",
      // OOM-killed, never resumed: the line ENDS at this ×, no dashed continuation.
      runs: [{ t0: t(12), t1: t(11), exitReason: "oom", exitCode: 137 }],
    },
    {
      id: "fx-sib1",
      row: 4,
      rootId: "fx-sib1",
      depth: 0,
      presence: "live",
      erased: false,
      label: "S-BE lane",
      kind: "opencode",
      origin: "schedule",
      runs: [{ t0: t(14), t1: null }],
      state: "unknown",
      raw: "reviewing", // observed, but a spelling this contract does not know
    },
    {
      // A deleted lane (ADR 0096 decision 6): lineage is gone, but its id still holds a
      // row so the peer arrow below does not misattribute as coming from outside the
      // figure. `label` carries the bare slug on purpose — it is the one place the
      // contract hands one out, and the view must compose the "deleted" wording around
      // it rather than print it alone.
      id: "fx-erased1",
      row: 5,
      rootId: "fx-erased1",
      depth: 0,
      presence: "gone",
      erased: true,
      label: "fx-erased1",
      runs: [],
    },
    {
      // The Agent went down before writing a death for this run (or the process was
      // killed outside the tmux pane wait): the run is CUT at the last observed instant
      // rather than left open forever. A hollow × — never a filled one, which would
      // claim a death that was never actually seen — and nothing drawn after it: the
      // stretch past the cut is unknown, not the resumable dashed line a `stopped`
      // presence would draw (ADR 0096 decision 12).
      id: "fx-cut1",
      row: 6,
      rootId: "fx-cut1",
      depth: 0,
      presence: "gone",
      erased: false,
      label: "unwatched probe",
      kind: "cursor",
      origin: "user",
      runs: [{ t0: t(9), t1: t(2), cut: true }],
    },
  ];

  const segments: GraphSegment[] = [
    // fx-root1: idle, then a gap nobody observed, then active (open, pulses at the edge).
    { laneId: "fx-root1", t0: t(20), t1: t(18), kind: "idle", state: "idle" },
    { laneId: "fx-root1", t0: t(8), t1: t(4), kind: "waiting", state: "question" },
    { laneId: "fx-root1", t0: t(4), t1: now, kind: "active", state: "working" },
    // fx-child1: never observed near birth (no Console open, no reaper — state absent),
    // then a normal working/idle alternation while it was watched.
    { laneId: "fx-child1", t0: t(19), t1: t(17), kind: "unknown" },
    { laneId: "fx-child1", t0: t(17), t1: t(9), kind: "active", state: "working" },
    { laneId: "fx-child1", t0: t(9), t1: t(3), kind: "idle", state: "idle" },
    // fx-child2: archived lane still carries its last run's bands.
    { laneId: "fx-child2", t0: t(15), t1: t(6), kind: "active", state: "compacting" },
    // fx-sib1: OBSERVED but unrecognised — "raw" rides on the segment too, for the
    // tooltip, distinct from the lane's live-state raw above.
    { laneId: "fx-sib1", t0: t(14), t1: t(10), kind: "unknown", state: "unknown", raw: "reviewing" },
    { laneId: "fx-sib1", t0: t(10), t1: now, kind: "active", state: "working" },
    { laneId: "fx-cut1", t0: t(9), t1: t(2), kind: "idle", state: "idle" },
  ];

  const arrows: GraphArrow[] = [
    // The operator's chat conversation instructs fx-root1 — leaves the figure's top edge
    // (decision 8-2: a conversation has no birth/death, so it never becomes a lane).
    { ts: t(20) - 300_000, variant: "instruct", from: "conv:fx-conv1", to: "fx-root1", fromRow: null, toRow: 0, x: xOf(scale, t(20) - 300_000), label: "Kick off the fleet graph P1 lanes" },
    // spawn: fx-root1 -> fx-child1, at the child's birth.
    { ts: t(19), variant: "spawn", from: "fx-root1", to: "fx-child1", fromRow: 0, toRow: 1, x: xOf(scale, t(19)) },
    // fork: fx-child1 -> fx-child2.
    { ts: t(15), variant: "fork", from: "fx-child1", to: "fx-child2", fromRow: 1, toRow: 2, x: xOf(scale, t(15)) },
    // handoff: a person launched fx-cut1 from a proposal fx-child2 made.
    { ts: t(9), variant: "handoff", from: "fx-child2", to: "fx-cut1", fromRow: 2, toRow: 6, x: xOf(scale, t(9)) },
    // scheduled execution starts fx-sib1 — another top-edge arrow, different actor.
    { ts: t(14), variant: "instruct", from: "schedule", to: "fx-sib1", fromRow: null, toRow: 4, x: xOf(scale, t(14)) },
    // fx-probe1's death is reported back to the conversation, red (an exit with a reason).
    { ts: t(11), variant: "report", from: "fx-probe1", to: "conv:fx-conv1", fromRow: 3, toRow: null, x: xOf(scale, t(11)), danger: true, label: "oom" },
    // peer round trip: fx-root1 <-> fx-sib1.
    { ts: t(10), variant: "peer", from: "fx-root1", to: "fx-sib1", fromRow: 0, toRow: 4, x: xOf(scale, t(10)), label: "request" },
    { ts: t(9.5), variant: "peer", from: "fx-sib1", to: "fx-root1", fromRow: 4, toRow: 0, x: xOf(scale, t(9.5)), label: "answer" },
    // peer touching the erased lane: the row must exist for this to draw as an in-figure
    // arrow rather than an external one (decision 6).
    { ts: t(5), variant: "peer", from: "fx-child1", to: "fx-erased1", fromRow: 1, toRow: 5, x: xOf(scale, t(5)), label: "notice" },
    // The auto-resume nudge that revives fx-root1's second run — attributed to "agent",
    // never to "user" (it is nobody's instruction).
    { ts: t(8), variant: "instruct", from: "agent", to: "fx-root1", fromRow: null, toRow: 0, x: xOf(scale, t(8)) },
    // fx-root1 reports back up to the conversation once its second run answers.
    { ts: t(1), variant: "report", from: "fx-root1", to: "conv:fx-conv1", fromRow: 0, toRow: null, x: xOf(scale, t(1)), label: "answer-ready" },
  ];

  const marks: CoverageMark[] = [{ t: t(18), kind: "activity-start", x: xOf(scale, t(18)) }];

  return {
    scale,
    lanes,
    segments,
    arrows,
    marks,
    height: lanes.length * scale.laneH,
  };
}
