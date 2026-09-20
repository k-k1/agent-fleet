import { describe, it, expect } from "vitest";
import { buildFleetGraph, normalizeState, segmentKindByState, xOf, laneY } from "./fleetgraph.ts";
import statesFixture from "./fleetgraph.states.json";
import type {
  ActivityEvent,
  BirthEvent,
  DeathEvent,
  FleetGraphPage,
  GraphLaneErased,
  GraphLaneKnown,
  GraphScale,
  LedgerState,
  LineageEvent,
} from "../types/fleetgraph.ts";
import type { Session } from "../types/session.ts";

// ── Fixture helpers ──────────────────────────────────────────────────────────

function birth(name: string, ts: number, overrides: Partial<BirthEvent> = {}): BirthEvent {
  return { ev: "birth", ts, name, kind: "claude", origin: "user", ...overrides };
}
function death(name: string, ts: number, overrides: Partial<DeathEvent> = {}): LineageEvent {
  return { ev: "death", ts, name, ...overrides };
}
function revive(name: string, ts: number): LineageEvent {
  return { ev: "revive", ts, name };
}
function archivedEv(name: string, ts: number, archived: boolean): LineageEvent {
  return { ev: "archived", ts, name, archived };
}
function convidEv(name: string, ts: number, conv: string): LineageEvent {
  return { ev: "convid", ts, name, conv };
}
function stateEv(name: string, ts: number, to: LedgerState, raw?: string): ActivityEvent {
  return { ev: "state", ts, name, to, ...(raw !== undefined ? { raw } : {}) };
}
function resyncEv(name: string, ts: number, to: LedgerState): ActivityEvent {
  return { ev: "resync", ts, name, to };
}
function instructEv(from: string, to: string, ts: number): ActivityEvent {
  return { ev: "instruct", ts, from, to };
}
function reportEv(from: string, to: string, ts: number, reason?: string): ActivityEvent {
  return { ev: "report", ts, from, to, ...(reason !== undefined ? { reason } : {}) };
}
function peerEv(from: string, to: string, ts: number): ActivityEvent {
  return { ev: "peer", ts, from, to, intent: "request" };
}
function mkSession(name: string, overrides: Partial<Session> = {}): Session {
  return { name, kind: "claude", ...overrides };
}
function sessMap(...sessions: Session[]): Map<string, Session> {
  return new Map(sessions.map((s) => [s.name, s]));
}
function mkPage(lineage: LineageEvent[], activity: ActivityEvent[] = [], overrides: Partial<FleetGraphPage> = {}): FleetGraphPage {
  return {
    since: 0,
    until: 100000,
    now: 100000,
    lineage,
    activity,
    coverage: { activitySince: null, lineageSince: null },
    ...overrides,
  };
}
function laneOf(model: ReturnType<typeof buildFleetGraph>, id: string): GraphLaneKnown | GraphLaneErased | undefined {
  return model.lanes.find((l) => l.id === id);
}
function isKnown(lane: GraphLaneKnown | GraphLaneErased | undefined): lane is GraphLaneKnown {
  return !!lane && lane.erased !== true;
}

// ── normalizeState / segmentKindByState — cross-checked against the frozen
// fixture BOTH this file and the Go writer read. Failing to find it must fail
// the suite, not skip it — a static JSON import achieves exactly that: a
// missing/malformed file breaks collection, which vitest reports red.
describe("normalizeState / segmentKindByState (fleetgraph.states.json)", () => {
  if (!statesFixture || !Array.isArray(statesFixture.cases) || statesFixture.cases.length === 0) {
    throw new Error("fleetgraph.states.json is missing or empty — the normalisation parity fixture must exist");
  }
  for (const c of statesFixture.cases) {
    it(`raw=${JSON.stringify(c.raw)} -> state=${c.state} band=${c.band}`, () => {
      const raw = c.raw === null ? undefined : c.raw;
      const state = normalizeState(raw);
      expect(state).toBe(c.state);
      expect(segmentKindByState[state]).toBe(c.band);
    });
  }
});

describe("xOf / laneY", () => {
  const scale: GraphScale = { from: 0, to: 1000, width: 500, laneH: 20 };
  it("maps linearly", () => {
    expect(xOf(scale, 0)).toBe(0);
    expect(xOf(scale, 1000)).toBe(500);
    expect(xOf(scale, 500)).toBe(250);
  });
  it("clamps outside the window", () => {
    expect(xOf(scale, -100)).toBe(0);
    expect(xOf(scale, 2000)).toBe(500);
  });
  it("laneY is row * laneH", () => {
    expect(laneY(scale, 0)).toBe(0);
    expect(laneY(scale, 3)).toBe(60);
  });
});

describe("buildFleetGraph — page may be null while the first load is in flight", () => {
  it("returns an empty model with a sane scale", () => {
    const model = buildFleetGraph(null, new Map(), { from: 0, to: 1000 });
    expect(model.lanes).toEqual([]);
    expect(model.segments).toEqual([]);
    expect(model.arrows).toEqual([]);
    expect(model.marks).toEqual([]);
    expect(model.height).toBe(0);
    expect(model.scale).toEqual({ from: 0, to: 1000, width: 1200, laneH: 28 });
  });
});

describe("buildFleetGraph — presence (the merge only both sources can decide)", () => {
  it("closed run + still listed = stopped", () => {
    const page = mkPage([birth("sA", 1000), death("sA", 5000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sA")), { from: 0, to: 10000 });
    const lane = laneOf(model, "sA");
    expect(isKnown(lane) && lane.presence).toBe("stopped");
  });

  it("closed run + not listed = gone", () => {
    const page = mkPage([birth("sA", 1000), death("sA", 5000)]);
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 10000 });
    const lane = laneOf(model, "sA");
    expect(isKnown(lane) && lane.presence).toBe("gone");
  });

  it("open run + listed = live", () => {
    const page = mkPage([birth("sA", 1000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sA")), { from: 0, to: 10000 });
    const lane = laneOf(model, "sA");
    expect(isKnown(lane) && lane.presence).toBe("live");
    expect(isKnown(lane) && lane.runs[0].t1).toBeNull();
  });

  it("open run + not listed = gone, and the run is cut at the last observed instant", () => {
    const page = mkPage([birth("sA", 1000)], [stateEv("sA", 3000, "working")]);
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 10000 });
    const lane = laneOf(model, "sA");
    expect(isKnown(lane) && lane.presence).toBe("gone");
    expect(isKnown(lane) && lane.runs[0]).toMatchObject({ t0: 1000, t1: 3000, cut: true });
  });

  it("archiving a still-running session (no death ever recorded) closes the run at the archived ts — a real ×, not a cut", () => {
    const page = mkPage([birth("sE", 0), archivedEv("sE", 1500, true)]); // no death event at all
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 5000 });
    const lane = laneOf(model, "sE");
    expect(isKnown(lane) && lane.presence).toBe("archived");
    expect(isKnown(lane) && lane.runs[0]).toEqual({ t0: 0, t1: 1500 }); // no cut, no exitReason — this end WAS observed
    const segs = model.segments.filter((s) => s.laneId === "sE").sort((a, b) => a.t0 - b.t0);
    expect(segs).toEqual([
      { laneId: "sE", t0: 0, t1: 1500, kind: "unknown" },
      { laneId: "sE", t0: 1500, t1: 5000, kind: "archived" },
    ]);
  });

  it("archived is always 'archived', unconditionally — the Agent's own list excludes archived sessions outright, so `live` is NEVER defined for one and a guard gated on the base presence would never fire", () => {
    const page = mkPage([birth("sD", 0), death("sD", 1000), archivedEv("sD", 1500, true)]);
    // The realistic shape: an archived session is never in the live map.
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 5000 });
    const lane = laneOf(model, "sD");
    expect(isKnown(lane) && lane.presence).toBe("archived");
  });
});

describe("buildFleetGraph — runs (decision 12, a lane's life is a sequence of runs)", () => {
  it("revive opens a second run; a 24h window on day 4 still gets both (lineage is never clipped)", () => {
    const page = mkPage([birth("sA", 1000), death("sA", 2000), revive("sA", 3000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sA")), { from: 10000, to: 20000 });
    const lane = laneOf(model, "sA");
    expect(isKnown(lane) && lane.presence).toBe("live");
    expect(isKnown(lane) && lane.runs).toEqual([
      { t0: 1000, t1: 2000 },
      { t0: 3000, t1: null },
    ]);
  });
});

describe("buildFleetGraph — synthesized lanes (a live session S-BE hasn't backfilled a birth for yet)", () => {
  it("builds a lane straight from the Session, origin 'unknown', label from displayName()", () => {
    const page = mkPage([]); // no lineage anywhere
    const t0 = Date.parse("2026-01-01T00:00:00.000Z");
    const sessions = sessMap(mkSession("sX", { title: "My Session", createdAt: "2026-01-01T00:00:00.000Z" }));
    const model = buildFleetGraph(page, sessions, { from: t0 - 1000, to: t0 + 5000 });
    const lane = laneOf(model, "sX");
    expect(isKnown(lane) && lane.origin).toBe("unknown");
    expect(isKnown(lane) && lane.label).toBe("My Session");
    expect(isKnown(lane) && lane.presence).toBe("live");
    expect(isKnown(lane) && lane.runs).toEqual([{ t0, t1: null }]);
  });

  it("with no createdAt either, the synthesized birth is pinned to the window's left edge — not a real instant", () => {
    const page = mkPage([]);
    const model = buildFleetGraph(page, sessMap(mkSession("sX")), { from: 2000, to: 5000 });
    const lane = laneOf(model, "sX");
    expect(isKnown(lane) && lane.runs).toEqual([{ t0: 2000, t1: null }]);
  });
});

describe("buildFleetGraph — live lane state/raw (the only place the builder normalizes a LIVE Session.state)", () => {
  it("no live state reported yet normalizes to idle, with no raw", () => {
    const page = mkPage([birth("sK", 1000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sK")), { from: 0, to: 5000 });
    const lane = laneOf(model, "sK");
    expect(isKnown(lane) && lane.state).toBe("idle");
    expect(isKnown(lane) && lane.raw).toBeUndefined();
  });

  it("an unrecognised live state normalizes to unknown, keeping raw for the tooltip", () => {
    const page = mkPage([birth("sK", 1000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sK", { state: "thinking" })), { from: 0, to: 5000 });
    const lane = laneOf(model, "sK");
    expect(isKnown(lane) && lane.state).toBe("unknown");
    expect(isKnown(lane) && lane.raw).toBe("thinking");
  });
});

describe("buildFleetGraph — segments", () => {
  it("no observation is unknown-with-no-state; compacting bands active; a resync blanks the stretch before it, even one that had a real observed-unknown spelling", () => {
    const page = mkPage(
      [birth("sB", 0)],
      [stateEv("sB", 1000, "compacting"), stateEv("sB", 2000, "unknown", "thinking"), resyncEv("sB", 3000, "idle")],
    );
    const model = buildFleetGraph(page, sessMap(mkSession("sB")), { from: 0, to: 5000 });
    const segs = model.segments.filter((s) => s.laneId === "sB").sort((a, b) => a.t0 - b.t0);
    expect(segs).toEqual([
      { laneId: "sB", t0: 0, t1: 1000, kind: "unknown" },
      { laneId: "sB", t0: 1000, t1: 2000, kind: "active", state: "compacting" },
      { laneId: "sB", t0: 2000, t1: 3000, kind: "unknown" },
      { laneId: "sB", t0: 3000, t1: 5000, kind: "idle", state: "idle" },
    ]);
  });

  it("a gap between two runs is 'stopped' regardless of the lane's current presence; a gone lane draws nothing past its last run", () => {
    const page = mkPage([birth("sC", 0), death("sC", 1000), revive("sC", 2000), death("sC", 3000)]);
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 5000 });
    const segs = model.segments.filter((s) => s.laneId === "sC").sort((a, b) => a.t0 - b.t0);
    expect(segs).toEqual([
      { laneId: "sC", t0: 0, t1: 1000, kind: "unknown" },
      { laneId: "sC", t0: 1000, t1: 2000, kind: "stopped" },
      { laneId: "sC", t0: 2000, t1: 3000, kind: "unknown" },
    ]);
  });

  it("an archived lane's trailing stretch bands 'archived', not 'stopped' or 'unknown' — even though it is never in the live map", () => {
    const page = mkPage([birth("sD", 0), death("sD", 1000), archivedEv("sD", 1500, true)]);
    const model = buildFleetGraph(page, sessMap(), { from: 0, to: 5000 });
    const segs = model.segments.filter((s) => s.laneId === "sD").sort((a, b) => a.t0 - b.t0);
    expect(segs).toEqual([
      { laneId: "sD", t0: 0, t1: 1000, kind: "unknown" },
      { laneId: "sD", t0: 1000, t1: 5000, kind: "archived" },
    ]);
  });
});

describe("buildFleetGraph — family order (decision 9)", () => {
  it("parent then children, siblings oldest-first, roots newest-birth-family-first", () => {
    const page = mkPage([
      birth("pP", 1000),
      birth("cOlder", 2000, { origin: "session", originSession: "pP" }),
      birth("cNewer", 3000, { origin: "session", originSession: "pP" }),
      birth("pQ", 500),
    ]);
    const sessions = sessMap(mkSession("pP"), mkSession("cOlder"), mkSession("cNewer"), mkSession("pQ"));
    const model = buildFleetGraph(page, sessions, { from: 0, to: 10000 });
    const order = model.lanes
      .slice()
      .sort((a, b) => a.row - b.row)
      .map((l) => l.id);
    expect(order).toEqual(["pP", "cOlder", "cNewer", "pQ"]);
  });

  it("a deleted intermediate parent's id still anchors rootId; a missing row doesn't collapse depth", () => {
    const page = mkPage([
      birth("root0", -10000),
      death("root0", -9000),
      birth("mid1", -8000, { origin: "session", originSession: "root0" }),
      death("mid1", -7000),
      birth("grand1", 1000, { origin: "session", originSession: "mid1" }),
    ]);
    const model = buildFleetGraph(page, sessMap(mkSession("grand1")), { from: 0, to: 5000 });
    expect(laneOf(model, "root0")).toBeUndefined(); // dead long before the window: no row
    expect(laneOf(model, "mid1")).toBeUndefined(); // same — depth 1 has no row here
    const grand = laneOf(model, "grand1");
    expect(isKnown(grand) && grand.parent).toBe("mid1");
    expect(isKnown(grand) && grand.rootId).toBe("root0");
    expect(isKnown(grand) && grand.depth).toBe(2); // still 2, not promoted to a root
  });

  it("a parent whose OWN birth was deleted becomes rootId itself, at depth 0", () => {
    const page = mkPage([birth("orphan", 1000, { origin: "session", originSession: "ghost" })]);
    const model = buildFleetGraph(page, sessMap(mkSession("orphan")), { from: 0, to: 5000 });
    const lane = laneOf(model, "orphan");
    expect(isKnown(lane) && lane.rootId).toBe("ghost");
    expect(isKnown(lane) && lane.depth).toBe(1);
    expect(laneOf(model, "ghost")).toBeUndefined(); // no lineage for it at all: no row
  });

  it("a family anchored on an unreachable root still sorts by its own members' oldest KNOWN birth, not swallowed to -Infinity", () => {
    const page = mkPage([
      birth("orphan", 5000, { origin: "session", originSession: "ghost" }), // recent, root's own birth is unknown
      birth("plain", 1000), // older, ordinary root with its own birth
    ]);
    const sessions = sessMap(mkSession("orphan"), mkSession("plain"));
    const model = buildFleetGraph(page, sessions, { from: 0, to: 10000 });
    const order = model.lanes
      .slice()
      .sort((a, b) => a.row - b.row)
      .map((l) => l.id);
    // orphan's family (age 5000, from orphan itself) is newer than plain's (age 1000):
    // it must sort first. Under the bug, the unreachable "ghost" root's missing birth
    // sank the whole family to -Infinity regardless of orphan's real age, and "plain"
    // sorted first instead.
    expect(order).toEqual(["orphan", "plain"]);
  });
});

describe("buildFleetGraph — erased lanes (decision 6)", () => {
  it("a lane with no lineage, kept alive by a surviving peer line, draws no runs/segments but still anchors the arrow", () => {
    const page = mkPage([birth("sY", 500)], [peerEv("sX", "sY", 2000)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sY")), { from: 0, to: 5000 });
    const erased = laneOf(model, "sX");
    expect(erased && erased.erased).toBe(true);
    expect(erased && (erased as GraphLaneErased).label).toBe("sX"); // the one place a bare slug is allowed
    expect(erased && erased.runs).toEqual([]);
    expect(model.segments.some((s) => s.laneId === "sX")).toBe(false);

    const arrow = model.arrows.find((a) => a.variant === "peer");
    expect(arrow?.fromRow).toBe(erased?.row);
    expect(arrow?.toRow).toBe(laneOf(model, "sY")?.row);
  });

  it("a state/resync-only reference to an unknown id draws no phantom row — only instruct/report/peer name an arrow endpoint", () => {
    const page = mkPage([], [stateEv("sPhantom", 1000, "working")]);
    const model = buildFleetGraph(page, new Map(), { from: 0, to: 5000 });
    expect(laneOf(model, "sPhantom")).toBeUndefined();
  });
});

describe("buildFleetGraph — label never puts the bare slug on screen alone (decision 5)", () => {
  it("a birth with neither display nor repo falls back to the kind, not the id", () => {
    const page = mkPage([birth("sZ", 1000, { kind: "codex" })]);
    const model = buildFleetGraph(page, sessMap(mkSession("sZ")), { from: 0, to: 5000 });
    const lane = laneOf(model, "sZ");
    expect(isKnown(lane) && lane.label).not.toContain("sZ");
    expect(isKnown(lane) && lane.label.startsWith("codex@")).toBe(true);
  });
});

describe("buildFleetGraph — arrows to a non-lane actor leave the figure (decision 8-2)", () => {
  it("an instruct from a conversation has fromRow null", () => {
    const page = mkPage([birth("sH", 1000)], [instructEv("conv:XYZ", "sH", 1500)]);
    const model = buildFleetGraph(page, sessMap(mkSession("sH")), { from: 0, to: 5000 });
    const arrow = model.arrows.find((a) => a.variant === "instruct");
    expect(arrow?.fromRow).toBeNull();
    expect(arrow?.toRow).toBe(laneOf(model, "sH")?.row);
  });

  it("a report with an exit reason is flagged danger", () => {
    const page = mkPage([birth("sH", 1000)], [reportEv("sH", "conv:XYZ", 1500, "oom")]);
    const model = buildFleetGraph(page, sessMap(mkSession("sH")), { from: 0, to: 5000 });
    const arrow = model.arrows.find((a) => a.variant === "report");
    expect(arrow?.danger).toBe(true);
    expect(arrow?.toRow).toBeNull();
  });

  it("an arrow whose endpoint is a REAL lane that got filtered out of THIS render is dropped, never shown as if it came from outside the figure", () => {
    const page = mkPage(
      [birth("sM", 1000), birth("sN", 1000)],
      [instructEv("conv:XYZ", "sM", 1500), peerEv("sM", "sN", 1600)],
    );
    const sessions = sessMap(mkSession("sM"), mkSession("sN"));
    const model = buildFleetGraph(page, sessions, { from: 0, to: 5000, conversationId: "XYZ" });
    expect(model.lanes.map((l) => l.id)).toEqual(["sM"]); // sN was not touched by conv:XYZ, so it has no row
    expect(model.arrows.some((a) => a.variant === "peer")).toBe(false); // dropped, not misattributed to an external actor
  });
});

describe("buildFleetGraph — birth arrows (spawn / handoff / fork, decision 13)", () => {
  it("origin=session draws a spawn arrow from the parent", () => {
    const page = mkPage([birth("pA", 1000), birth("cA", 2000, { origin: "session", originSession: "pA" })]);
    const model = buildFleetGraph(page, sessMap(mkSession("pA"), mkSession("cA")), { from: 0, to: 5000 });
    const arrow = model.arrows.find((a) => a.variant === "spawn");
    expect(arrow).toMatchObject({ from: "pA", to: "cA", ts: 2000 });
    expect(arrow?.fromRow).toBe(laneOf(model, "pA")?.row);
    expect(arrow?.toRow).toBe(laneOf(model, "cA")?.row);
  });

  it("origin=handoff draws a handoff arrow from the predecessor", () => {
    const page = mkPage([birth("pB", 1000), birth("cB", 2000, { origin: "handoff", originSession: "pB" })]);
    const model = buildFleetGraph(page, sessMap(mkSession("pB"), mkSession("cB")), { from: 0, to: 5000 });
    const arrow = model.arrows.find((a) => a.variant === "handoff");
    expect(arrow).toMatchObject({ from: "pB", to: "cB" });
  });

  it("a spawn arrow survives even when the parent has no row this render — fromRow: null marks 'the parent exists but is not drawn', not 'left the figure'", () => {
    const page = mkPage([
      birth("pC", -10000), // born long before the window, dead long before it too: no row
      death("pC", -9000),
      birth("cC", 1000, { origin: "session", originSession: "pC" }), // spawned in-window
    ]);
    const model = buildFleetGraph(page, sessMap(mkSession("cC")), { from: 0, to: 5000 });
    expect(laneOf(model, "pC")).toBeUndefined(); // confirms the parent really has no row
    const arrow = model.arrows.find((a) => a.variant === "spawn");
    expect(arrow).toMatchObject({ from: "pC", to: "cC" });
    expect(arrow?.fromRow).toBeNull();
    expect(arrow?.toRow).toBe(laneOf(model, "cC")?.row);
  });

  it("fork resolves forkFrom against the conv id IN FORCE at the child's birth, drifting through convid", () => {
    const page = mkPage([
      birth("sG", 1000, { conv: "conv-old" }),
      convidEv("sG", 1500, "conv-new"),
      birth("early", 1200, { forkFrom: "conv-old" }), // before the drift: still "conv-old"
      birth("late", 2000, { forkFrom: "conv-new" }), // after the drift: "conv-new"
      birth("nomatch", 2500, { forkFrom: "conv-nobody" }),
    ]);
    const sessions = sessMap(mkSession("sG"), mkSession("early"), mkSession("late"), mkSession("nomatch"));
    const model = buildFleetGraph(page, sessions, { from: 0, to: 5000 });
    const forkFor = (to: string) => model.arrows.find((a) => a.variant === "fork" && a.to === to);
    expect(forkFor("early")?.from).toBe("sG");
    expect(forkFor("late")?.from).toBe("sG");
    const unresolved = forkFor("nomatch");
    expect(unresolved?.from).toBe("conv:conv-nobody"); // ActorId's conversation vocabulary, never a bare id
    expect(unresolved?.fromRow).toBeNull();
  });
});

describe("buildFleetGraph — options", () => {
  it("conversationId keeps only the lanes that conversation touched", () => {
    const page = mkPage([birth("sH", 1000), birth("sI", 1000)], [instructEv("conv:XYZ", "sH", 1500)]);
    const sessions = sessMap(mkSession("sH"), mkSession("sI"));
    const filtered = buildFleetGraph(page, sessions, { from: 0, to: 5000, conversationId: "XYZ" });
    expect(filtered.lanes.map((l) => l.id)).toEqual(["sH"]);

    const unfiltered = buildFleetGraph(page, sessions, { from: 0, to: 5000 });
    expect(unfiltered.lanes.map((l) => l.id).sort()).toEqual(["sH", "sI"]);
  });

  it("showArchived:false drops archived lanes", () => {
    const page = mkPage([birth("sJ", 0), death("sJ", 1000), archivedEv("sJ", 1500, true)]);
    const sessions = sessMap(); // archived sessions never appear in the live map
    const shown = buildFleetGraph(page, sessions, { from: 0, to: 5000, showArchived: true });
    expect(shown.lanes.map((l) => l.id)).toEqual(["sJ"]);
    const hidden = buildFleetGraph(page, sessions, { from: 0, to: 5000, showArchived: false });
    expect(hidden.lanes.map((l) => l.id)).toEqual([]);
  });

  it("jitter shifts an edge-clustered group inward AS A WHOLE — never outside [0, width], and never collapsed back onto each other", () => {
    const names = ["s1", "s2", "s3", "s4", "s5"];
    const page = mkPage(
      names.map((n) => birth(n, 1000)),
      names.map((n, i) => instructEv(`conv:${i}`, n, 0)), // all at the window's very left edge
    );
    const sessions = sessMap(...names.map((n) => mkSession(n)));
    const model = buildFleetGraph(page, sessions, { from: 0, to: 5000, width: 1200 });
    expect(model.arrows.length).toBe(5);
    const xs = model.arrows.map((a) => a.x).sort((a, b) => a - b);
    for (const x of xs) {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(1200);
    }
    expect(new Set(xs).size).toBe(5); // a per-point clamp would collapse the outer members back onto 0
  });
});

describe("buildFleetGraph — coverage marks (decision 8)", () => {
  it("emits a mark per boundary inside the window, sorted, and drops ones outside it", () => {
    const page = mkPage([], [], { coverage: { activitySince: 500, lineageSince: 100, backfilledBefore: -100 } });
    const model = buildFleetGraph(page, new Map(), { from: 0, to: 1000 });
    expect(model.marks.map((m) => m.kind)).toEqual(["lineage-start", "activity-start"]);
    expect(model.marks[0]).toMatchObject({ t: 100, kind: "lineage-start" });
    expect(model.marks[1]).toMatchObject({ t: 500, kind: "activity-start" });
  });
});
