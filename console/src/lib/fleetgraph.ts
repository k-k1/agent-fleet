// Fleet session graph layout (ADR 0096 / docs/log/101) — the pure builder that
// turns a ledger page (history) plus the live sessions map (now) into a render
// model. Structural template borrowed from lib/gitgraph.ts: pure functions in,
// plain-data model out, no methods on the model itself.
import type { Session, SessionKind } from "../types/session.ts";
import { displayName } from "./sessionview.ts";
import type {
  ActorId,
  ArchivedEvent,
  ArrowVariant,
  BirthEvent,
  BuildFleetGraph,
  BuildFleetGraphOptions,
  CoverageMark,
  FleetGraphPage,
  GraphArrow,
  GraphLane,
  GraphLaneErased,
  GraphLaneKnown,
  GraphLaneY,
  GraphModel,
  GraphIsExternalActor,
  GraphNormalizeState,
  GraphOrigin,
  GraphScale,
  GraphSegment,
  GraphXOf,
  LaneId,
  LanePresence,
  LaneRun,
  LedgerState,
  LineageEvent,
  SegmentKind,
  SegmentKindByState,
} from "../types/fleetgraph.ts";

const DAY_MS = 24 * 60 * 60 * 1000;
const DEFAULT_WIDTH = 1200;
const DEFAULT_LANE_H = 28;

// ── Normalisation (decision 3 / decision 12) ────────────────────────────────
// The full LedgerState vocabulary minus the two spellings that never arrive as
// a raw string ("" and undefined map to idle before this table is consulted).
const LEDGER_STATES: readonly LedgerState[] = [
  "working",
  "compacting",
  "idle",
  "question",
  "plan",
  "permission",
  "blocked",
  "auth",
  "limited",
  "spend_limit",
  "failed",
  "aborted",
  "unknown",
];
const KNOWN_STATES = new Set<string>(LEDGER_STATES);

// normalizeState: "" and undefined -> idle, an unrecognised spelling -> unknown
// (never idle — see the frozen contract's header note on why that direction is
// the dangerous one). Exact match only, no case folding (fleetgraph.states.json).
export const normalizeState: GraphNormalizeState = (raw) => {
  if (raw === undefined || raw === "") return "idle";
  return KNOWN_STATES.has(raw) ? (raw as LedgerState) : "unknown";
};

// The coarse band a state paints. Frozen shape (types/fleetgraph.ts comment) —
// "stopped" is NOT reachable through this table: the builder assigns it directly
// for the stretch between (and after) a lane's runs.
export const segmentKindByState: SegmentKindByState = {
  working: "active",
  compacting: "active",
  idle: "idle",
  question: "waiting",
  plan: "waiting",
  permission: "waiting",
  blocked: "waiting",
  auth: "waiting",
  limited: "waiting",
  spend_limit: "waiting",
  failed: "idle",
  aborted: "idle",
  unknown: "unknown",
};

export const xOf: GraphXOf = (scale, ts) => {
  const span = scale.to - scale.from;
  if (span <= 0) return 0;
  const clamped = ts < scale.from ? scale.from : ts > scale.to ? scale.to : ts;
  return ((clamped - scale.from) / span) * scale.width;
};

export const laneY: GraphLaneY = (scale, row) => row * scale.laneH;

// ── External actors (decision 8-2) ──────────────────────────────────────────
// Only a LaneId becomes a lane. These spellings never do, however they arrive.
//
// Exported because the view needs the same answer to tell decision 8-2's "no lane at
// all" from decision 9's "lane exists, not drawn here", and a second copy of the list
// is a defect the moment the vocabulary grows: the view would stop recognising the new
// spelling, read a family arrow's missing parent as an external sender, and drop it
// without a sound. The contract types it as GraphIsExternalActor.
export const isExternalActor: GraphIsExternalActor = (id) => {
  if (id === "user" || id === "schedule" || id === "agent") return true;
  return id.startsWith("conv:") || id.startsWith("bridge:");
};

// stampMs: MMDD-HHMM from a millis instant (lib/sessionview.ts's `stamp`, ported
// to the millis-everywhere contract this file speaks instead of an ISO string) —
// used only for a lane whose birth carries no `display` (title/label are not on
// BirthEvent at all; a live session with NO birth uses the full displayName()
// from sessionview.ts instead, imported above).
function stampMs(ms: number | undefined): string {
  if (ms === undefined || Number.isNaN(ms)) return "";
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

// ── Runs (decision 12) ───────────────────────────────────────────────────────
// A lane's lineage, replayed in order, turns into a sequence of runs: birth or
// revive opens one, death closes it. convid/archived events don't affect runs.
function buildRuns(events: LineageEvent[]): LaneRun[] {
  const runs: LaneRun[] = [];
  let current: LaneRun | null = null;
  for (const ev of events) {
    if (ev.ev === "birth") {
      current = { t0: ev.ts, t1: null };
      runs.push(current);
    } else if (ev.ev === "revive") {
      if (!current || current.t1 !== null) {
        current = { t0: ev.ts, t1: null };
        runs.push(current);
      }
    } else if (ev.ev === "death") {
      if (current && current.t1 === null) {
        current.t1 = ev.ts;
        if (ev.reason !== undefined) current.exitReason = ev.reason;
        if (ev.code !== undefined) current.exitCode = ev.code;
        if (ev.signal !== undefined) current.exitSignal = ev.signal;
      }
    }
  }
  return runs;
}

// presenceFor: the merge decision that needs BOTH the ledger and the live map.
// A run left open by the ledger is "live" only while the lane is still listed;
// once it drops off the list with no death ever recorded, that run is cut
// (below) and the lane is "gone". A run the ledger closed (death, no revive
// after) is "stopped" while still listed, "gone" once it is not. `archived`
// ALWAYS wins, unconditionally: the Agent's own session list excludes
// archived sessions outright (sessionx/session_handlers.go's list handler
// skips them), so `live` is never defined for an archived lane — a guard
// that only applied the flag while some OTHER presence held would never
// fire in practice, and every archived lane would render as "gone" instead
// (an "id の痕跡だけ残る" lane, when it is really "畳んであるが復元できる").
// A known lane (this function's caller) always still has its lineage — a
// deletion removes it entirely and turns the lane erased, never leaves a
// known lane with a stale `archived` flag — so there is no case here where
// honouring `archived` misrepresents a truly gone lane.
function presenceFor(runs: LaneRun[], live: Session | undefined, archived: boolean): LanePresence {
  if (archived) return "archived";
  const last = runs[runs.length - 1];
  const open = last !== undefined && last.t1 === null;
  return open ? (live ? "live" : "gone") : live ? "stopped" : "gone";
}

// ── Fork resolution (decision 13) ───────────────────────────────────────────
// ForkFrom names a CONVERSATION id, not a lane. Resolving it means asking every
// other lane "which conversation id were you writing under at this instant?" —
// conv ids drift (birth.conv, then a ConvIdEvent per drift) so the answer is a
// point-in-time lookup, never a constant.
interface ConvSpan {
  ts: number;
  conv: string;
}

function convHistory(events: LineageEvent[]): ConvSpan[] {
  const out: ConvSpan[] = [];
  for (const ev of events) {
    if (ev.ev === "birth" && ev.conv) out.push({ ts: ev.ts, conv: ev.conv });
    else if (ev.ev === "convid") out.push({ ts: ev.ts, conv: ev.conv });
  }
  return out; // events arrive pre-sorted by ts (caller sorts the lane's lineage once)
}

// The conversation id in force for a lane at instant `t`: the latest span whose
// ts <= t, or undefined if the lane had no conv id yet at that time.
function convAt(history: ConvSpan[], t: number): string | undefined {
  let cur: string | undefined;
  for (const span of history) {
    if (span.ts > t) break;
    cur = span.conv;
  }
  return cur;
}

// ── Family order (decision 9) ───────────────────────────────────────────────
// Parent directly above its children, siblings oldest-first, roots newest-first
// (ADR 0078 decision 6, borrowed). The chain is walked through `parentOf`, which
// may point at an id with no birth of its own (deleted parent, decision 6) —
// THAT id becomes rootId even though it never gets a row (decision 9).
interface RootInfo {
  rootId: LaneId;
  depth: number;
}

// resolveRoots: DFS with a grey/black colouring so a corrupted originSession
// cycle stops at the point it closes rather than recursing forever (same hazard
// lib/project.ts's sessionLineages guards against).
function resolveRoots(ids: Iterable<LaneId>, parentOf: Map<LaneId, LaneId | undefined>): Map<LaneId, RootInfo> {
  const result = new Map<LaneId, RootInfo>();
  const resolving = new Set<LaneId>();
  const bump = (p: RootInfo): RootInfo => ({ rootId: p.rootId, depth: p.depth + 1 });
  function resolve(id: LaneId): RootInfo {
    const cached = result.get(id);
    if (cached) return cached;
    if (resolving.has(id)) {
      const info: RootInfo = { rootId: id, depth: 0 };
      result.set(id, info);
      return info;
    }
    resolving.add(id);
    const parent = parentOf.get(id);
    const info: RootInfo = parent === undefined ? { rootId: id, depth: 0 } : bump(resolve(parent));
    resolving.delete(id);
    result.set(id, info);
    return info;
  }
  for (const id of ids) resolve(id);
  return result;
}

// familyOrder: every id reachable from a root, parent then children (siblings
// oldest birth first — missing birth sorts as oldest, i.e. first), roots
// ordered by the OLDEST known birth anywhere in the family, newest family
// first — the root's own birth may not exist (decision 9), so the key has to
// be one that always does.
function familyOrder(
  rootIds: Iterable<LaneId>,
  childrenOf: Map<LaneId, LaneId[]>,
  birthTs: (id: LaneId) => number | undefined,
): LaneId[] {
  const ageKey = (id: LaneId): number => birthTs(id) ?? Number.NEGATIVE_INFINITY;
  const out: LaneId[] = [];
  const seen = new Set<LaneId>();
  function dfs(id: LaneId) {
    if (seen.has(id)) return;
    seen.add(id);
    out.push(id);
    const kids = (childrenOf.get(id) ?? []).slice().sort((a, b) => ageKey(a) - ageKey(b) || (a < b ? -1 : a > b ? 1 : 0));
    for (const k of kids) dfs(k);
  }
  // A member with no known birth (a virtual/deleted ancestor, an erased lane)
  // must NOT drag the whole family down to "oldest": it contributes nothing
  // to the minimum rather than a sentinel, so a family anchored on an
  // unreachable root still sorts by its actual descendants' ages. Only a
  // family where NOBODY has a known birth falls back to -Infinity.
  function familyMinBirth(id: LaneId, guard: Set<LaneId>): number | undefined {
    if (guard.has(id)) return undefined;
    guard.add(id);
    let min = birthTs(id);
    for (const k of childrenOf.get(id) ?? []) {
      const childMin = familyMinBirth(k, guard);
      if (childMin !== undefined) min = min === undefined ? childMin : Math.min(min, childMin);
    }
    return min;
  }
  const roots = [...new Set(rootIds)].sort((a, b) => {
    const ka = familyMinBirth(a, new Set()) ?? Number.NEGATIVE_INFINITY;
    const kb = familyMinBirth(b, new Set()) ?? Number.NEGATIVE_INFINITY;
    return ka !== kb ? kb - ka : a < b ? -1 : a > b ? 1 : 0;
  });
  for (const r of roots) dfs(r);
  return out;
}

// ── Segments (decision 3 / decision 12) ─────────────────────────────────────
interface StateMarker {
  ts: number;
  state: LedgerState;
  raw?: string;
  resetsBefore: boolean; // resync: force the stretch before it to "unknown", never bridge a restart gap
}

function pushClippedState(
  out: GraphSegment[],
  laneId: LaneId,
  t0: number,
  t1: number,
  state: LedgerState | undefined,
  raw: string | undefined,
  from: number,
  to: number,
) {
  const a = Math.max(t0, from);
  const b = Math.min(t1, to);
  if (a >= b) return;
  if (state === undefined) {
    out.push({ laneId, t0: a, t1: b, kind: "unknown" });
    return;
  }
  const seg: GraphSegment = { laneId, t0: a, t1: b, kind: segmentKindByState[state], state };
  if (state === "unknown" && raw !== undefined) seg.raw = raw;
  out.push(seg);
}

function pushClippedKind(out: GraphSegment[], laneId: LaneId, t0: number, t1: number, kind: SegmentKind, from: number, to: number) {
  const a = Math.max(t0, from);
  const b = Math.min(t1, to);
  if (a >= b) return;
  out.push({ laneId, t0: a, t1: b, kind });
}

// Bands inside ONE run: state persists from marker to marker, and a resync
// forces the stretch immediately before it to "unknown" regardless of what the
// prior marker said — an Agent restart writes nothing during the outage, so
// without this the stale pre-restart state would bridge straight across it.
function stateBandsWithinRun(laneId: LaneId, runStart: number, runEnd: number, markers: StateMarker[], from: number, to: number): GraphSegment[] {
  const relevant = markers.filter((m) => m.ts >= runStart && m.ts < runEnd);
  const out: GraphSegment[] = [];
  let cursor = runStart;
  let curState: LedgerState | undefined;
  let curRaw: string | undefined;
  for (const m of relevant) {
    const kind = m.resetsBefore ? undefined : curState;
    pushClippedState(out, laneId, cursor, m.ts, kind, curRaw, from, to);
    cursor = m.ts;
    curState = m.state;
    curRaw = m.raw;
  }
  pushClippedState(out, laneId, cursor, runEnd, curState, curRaw, from, to);
  return out;
}

function coalesceSegments(segs: GraphSegment[]): GraphSegment[] {
  const sorted = segs.slice().sort((a, b) => a.t0 - b.t0);
  const out: GraphSegment[] = [];
  for (const s of sorted) {
    const prev = out[out.length - 1];
    if (prev && prev.t1 === s.t0 && prev.kind === s.kind && prev.state === s.state && prev.raw === s.raw) {
      prev.t1 = s.t1;
    } else {
      out.push({ ...s });
    }
  }
  return out;
}

// segmentsForLane: state bands inside each run, plus a "stopped" band in every
// gap between two runs (it really was stopped-then-resumed, whatever the lane's
// CURRENT presence is), plus one trailing band after the last run for the
// lane's current presence — except when that presence is "gone", where the
// line simply ends (decision 12: no band survives past a prune/delete).
function segmentsForLane(
  laneId: LaneId,
  runs: LaneRun[],
  presence: LanePresence,
  markers: StateMarker[],
  from: number,
  to: number,
): GraphSegment[] {
  const out: GraphSegment[] = [];
  for (let i = 0; i < runs.length; i++) {
    const run = runs[i];
    const runEnd = run.t1 ?? to;
    out.push(...stateBandsWithinRun(laneId, run.t0, runEnd, markers, from, to));
    if (run.t1 === null) continue; // still open: nothing after it to fill
    const isLast = i === runs.length - 1;
    if (isLast) {
      // Only "stopped" fills the stretch to the right edge. An ARCHIVED lane draws
      // nothing past its × (decision 12's 2026-09-21 amendment, with the dashed tail the
      // view used to add): a lane somebody folded away should not keep painting a band
      // across the whole figure as loudly as a live one. That it is archived rather than
      // gone is said in words, by the state chip in the label column.
      if (presence === "stopped") pushClippedKind(out, laneId, run.t1, to, "stopped", from, to);
      // presence === "archived" / "gone": the line ends at run.t1, nothing after it.
    } else {
      pushClippedKind(out, laneId, run.t1, runs[i + 1].t0, "stopped", from, to);
    }
  }
  return coalesceSegments(out);
}

// ── Arrow x-jitter (docs/log/101 §101.4, "same problem as the commit graph's
// crossing edges, same fix") — simultaneous arrows at the same instant would
// stack exactly on top of each other; spread them a few px apart, centred.
// The bucket is wider than 1px (not `Math.round(x)`) so two arrows a
// fraction of a pixel apart still count as "the same spot" and get spread —
// otherwise a near-miss rounds into different buckets and draws un-jittered,
// which looks identical to the problem this exists to fix. A cluster that
// would spread past [0, width] is shifted INWARD AS A WHOLE, not clamped
// point by point: clamping each point individually collapses the outer
// members back onto the edge — the exact overlap this function exists to
// remove — while a uniform shift keeps every member's spacing intact.
function jitterX(rawX: number[], width: number): number[] {
  const BUCKET_PX = 2;
  const buckets = new Map<number, number[]>();
  rawX.forEach((x, i) => {
    const key = Math.round(x / BUCKET_PX);
    const arr = buckets.get(key) ?? [];
    arr.push(i);
    buckets.set(key, arr);
  });
  const out = rawX.slice();
  const SPREAD = 3;
  for (const idxs of buckets.values()) {
    if (idxs.length <= 1) continue;
    const n = idxs.length;
    const spread = idxs.map((i, k) => rawX[i] + (k - (n - 1) / 2) * SPREAD);
    const lo = Math.min(...spread);
    const hi = Math.max(...spread);
    const shift = lo < 0 ? -lo : hi > width ? width - hi : 0;
    idxs.forEach((i, k) => {
      out[i] = spread[k] + shift;
    });
  }
  return out;
}

interface LaneFacts {
  runs: LaneRun[];
  presence: LanePresence;
  kind: SessionKind;
  origin: GraphOrigin;
  label: string;
  parent?: LaneId;
  state?: LedgerState;
  raw?: string;
}

function parseCreatedAt(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? undefined : t;
}

// ── The builder ──────────────────────────────────────────────────────────────
export const buildFleetGraph: BuildFleetGraph = (
  page: FleetGraphPage | null,
  sessions: Map<string, Session>,
  opts: BuildFleetGraphOptions = {},
): GraphModel => {
  const to = opts.to ?? page?.now ?? Date.now();
  const from = opts.from ?? to - DAY_MS;
  const width = opts.width ?? DEFAULT_WIDTH;
  const laneH = opts.laneH ?? DEFAULT_LANE_H;
  const showArchived = opts.showArchived ?? true;
  const scale: GraphScale = { from, to, width, laneH };

  if (!page) {
    return { scale, lanes: [], segments: [], arrows: [], marks: [], height: 0 };
  }

  // 1. Lineage grouped by lane, sorted by ts; birth event per lane.
  const byLane = new Map<LaneId, LineageEvent[]>();
  for (const ev of page.lineage) {
    const arr = byLane.get(ev.name);
    if (arr) arr.push(ev);
    else byLane.set(ev.name, [ev]);
  }
  for (const arr of byLane.values()) arr.sort((a, b) => a.ts - b.ts);
  const birthOf = new Map<LaneId, BirthEvent>();
  for (const [id, events] of byLane) {
    const b = events.find((e): e is BirthEvent => e.ev === "birth");
    if (b) birthOf.set(id, b);
  }

  // 2. Activity: per-lane state/resync markers, a reference index (touched
  // ids, in-window or not — feeds `cut` and the overlap test), and a
  // NARROWER index of ids an ARROW actually names in-window (instruct /
  // report / peer only). Only the latter can turn into an erased row: decision
  // 6 exists to keep an arrow from misattributing to "outside the figure",
  // and a state/resync line alone never draws an arrow, so an id known only
  // through one is not a session that ever needs a row — just noise.
  const markersByLane = new Map<LaneId, StateMarker[]>();
  const activityRef = new Map<LaneId, { max: number; inWindow: boolean }>();
  const arrowNamed = new Set<LaneId>();
  const touch = (id: string, ts: number) => {
    if (isExternalActor(id)) return;
    const rec = activityRef.get(id) ?? { max: -Infinity, inWindow: false };
    rec.max = Math.max(rec.max, ts);
    if (ts >= from && ts <= to) rec.inWindow = true;
    activityRef.set(id, rec);
  };
  const touchArrow = (id: string, ts: number) => {
    if (isExternalActor(id)) return;
    if (ts >= from && ts <= to) arrowNamed.add(id);
  };
  for (const ev of page.activity) {
    if (ev.ev === "state" || ev.ev === "resync") {
      touch(ev.name, ev.ts);
      const arr = markersByLane.get(ev.name) ?? [];
      arr.push({ ts: ev.ts, state: ev.to, raw: ev.raw, resetsBefore: ev.ev === "resync" });
      markersByLane.set(ev.name, arr);
    } else if (ev.ev === "instruct" || ev.ev === "report" || ev.ev === "peer") {
      touch(ev.from, ev.ts);
      touch(ev.to, ev.ts);
      touchArrow(ev.from, ev.ts);
      touchArrow(ev.to, ev.ts);
    }
  }
  for (const arr of markersByLane.values()) arr.sort((a, b) => a.ts - b.ts);

  // 3. Classify: known (has a birth), synthesized (live only — S-BE hasn't
  // backfilled a birth for it yet, a defensive backstop, not the normal path),
  // erased (no birth, not live, kept alive only by a surviving arrow line —
  // decision 6's "arrows-only" row).
  //
  // knownIds is every id with a birth ANYWHERE in page.lineage — this
  // includes ancestor-only ids the server sent purely for family-ordering
  // context (FleetGraphPage's rule 3: an ancestor whose own life does not
  // overlap the window is context, never a row). Those never get a row —
  // `overlapsWindow` below filters them out — and `allLaneIds` (§9) inherits
  // the same mix on purpose: an ancestor with no overlapping run legitimately
  // has no row either way.
  const knownIds = new Set<LaneId>(birthOf.keys());
  const synthesizedIds = new Set<LaneId>();
  for (const id of sessions.keys()) if (!knownIds.has(id)) synthesizedIds.add(id);
  const erasedIds = new Set<LaneId>();
  for (const id of arrowNamed) {
    if (knownIds.has(id) || synthesizedIds.has(id)) continue;
    erasedIds.add(id);
  }

  // 4. Facts per known/synthesized lane: runs, presence, cut, and the fields a
  // GraphLaneKnown needs. Erased lanes carry none of this (decision 6).
  const facts = new Map<LaneId, LaneFacts>();
  for (const id of knownIds) {
    const events = byLane.get(id) ?? [];
    const birth = birthOf.get(id)!;
    const runs = buildRuns(events);
    const archivedEv = events.filter((e): e is ArchivedEvent => e.ev === "archived").pop();
    const live = sessions.get(id);
    const presence = presenceFor(runs, live, archivedEv?.archived === true);
    const last = runs[runs.length - 1];
    if (presence === "archived" && last && last.t1 === null) {
      // Archiving can fold away a still-RUNNING session with no death ever
      // recorded: HandleArchiveSession kills the pane directly, and the one
      // place that would notice the death (the list handler) skips archived
      // sessions outright, so it never gets the chance. The archived ts IS
      // the observed end here — a normal ×, not `cut` (cut means nobody ever
      // recorded an end at all; this end was explicitly observed). This also
      // defends a back-filled/legacy ledger where an old archive genuinely
      // preceded its death event. That same "defends a broken ledger"
      // premise cuts both ways: a `revive` with no `archived:false` restore
      // in between (also broken) reopens a run whose t0 postdates this
      // archived ts, and closing it at the archived ts unclamped would
      // produce t1 < t0 — a run that ends before it starts, drawn as an ×
      // to the LEFT of its own ○. Floor it at t0 instead.
      last.t1 = Math.max(last.t0, archivedEv!.ts);
    } else if (presence === "gone" && last && last.t1 === null) {
      const observed = Math.max(last.t0, events[events.length - 1]?.ts ?? last.t0, activityRef.get(id)?.max ?? -Infinity);
      last.t1 = observed;
      last.cut = true;
    }
    // repo, never `id` — a bare slug must never stand alone in a label
    // (decision 5), and the kind name is the only other thing on hand.
    const label = birth.display ?? `${birth.repo ?? birth.kind}@${stampMs(birth.ts)}`;
    const f: LaneFacts = { runs, presence, kind: birth.kind, origin: birth.origin, label, parent: birth.originSession };
    if (presence === "live") {
      f.state = normalizeState(live?.state);
      if (f.state === "unknown") f.raw = live?.state;
    }
    facts.set(id, f);
  }
  for (const id of synthesizedIds) {
    const live = sessions.get(id)!;
    const t0 = parseCreatedAt(live.createdAt) ?? from;
    const runs: LaneRun[] = [{ t0, t1: null }];
    const presence = presenceFor(runs, live, false);
    const f: LaneFacts = { runs, presence, kind: live.kind, origin: "unknown", label: displayName(live), parent: live.originSession };
    if (presence === "live") {
      f.state = normalizeState(live.state);
      if (f.state === "unknown") f.raw = live.state;
    }
    facts.set(id, f);
  }

  // 5. Family tree: parent links from birth.originSession (known) or the live
  // Session.originSession (synthesized) — erased lanes carry no lineage, so no
  // parent link exists for them (decision 6).
  const parentOf = new Map<LaneId, LaneId | undefined>();
  for (const id of knownIds) {
    const p = facts.get(id)!.parent;
    if (p) parentOf.set(id, p);
  }
  for (const id of synthesizedIds) {
    const p = facts.get(id)!.parent;
    if (p) parentOf.set(id, p);
  }
  const childrenOf = new Map<LaneId, LaneId[]>();
  for (const [child, parent] of parentOf) {
    if (!parent) continue;
    const arr = childrenOf.get(parent) ?? [];
    arr.push(child);
    childrenOf.set(parent, arr);
  }
  const allCandidateIds = new Set<LaneId>([...knownIds, ...synthesizedIds, ...erasedIds]);
  const rootInfos = resolveRoots(allCandidateIds, parentOf);
  const rootIdsSet = new Set<LaneId>([...rootInfos.values()].map((r) => r.rootId));
  const birthTsFn = (id: LaneId): number | undefined =>
    birthOf.get(id)?.ts ?? (synthesizedIds.has(id) ? parseCreatedAt(sessions.get(id)?.createdAt) : undefined);
  const orderedAll = familyOrder(rootIdsSet, childrenOf, birthTsFn);

  // 6. Inclusion: a lane earns a row only if its own life overlaps the window,
  // or some in-window activity names it (an erased lane's only way in — it has
  // no runs of its own). showArchived and conversationId narrow further.
  let touchedIds: Set<LaneId> | null = null;
  if (opts.conversationId) {
    const marker = `conv:${opts.conversationId}`;
    touchedIds = new Set<LaneId>();
    for (const ev of page.activity) {
      if (ev.ev === "instruct" && ev.from === marker) touchedIds.add(ev.to);
      else if (ev.ev === "report" && ev.to === marker) touchedIds.add(ev.from);
    }
  }
  const overlapsWindow = (id: LaneId): boolean => {
    const runs = facts.get(id)?.runs ?? [];
    if (runs.some((r) => r.t0 <= to && (r.t1 === null || r.t1 >= from))) return true;
    return activityRef.get(id)?.inWindow === true;
  };
  const included = (id: LaneId): boolean => {
    if (!overlapsWindow(id)) return false;
    const presence = facts.get(id)?.presence ?? "gone"; // erased -> gone
    if (!showArchived && presence === "archived") return false;
    if (touchedIds && !touchedIds.has(id)) return false;
    return true;
  };
  // 6-2. Family fold: a lane under a collapsed parent loses its row, and `row` is
  // renumbered over what survives — the rule showArchived set (filtering a second
  // time in the view would hide a builder bug behind the view's own filter).
  //
  // The fold hangs off the DRAWN parent only. An ancestor with no row this render
  // (off-window, or itself filtered out) folds nothing: its "+" is not on screen, so
  // hiding its descendants would leave rows missing with no press that brings them
  // back. That is also why `hasChildren` is counted over the ancestors that have a
  // row rather than over `childrenOf` — a parent whose every child is outside the
  // window would otherwise offer a "+" that does nothing.
  const openOrder = orderedAll.filter(included);
  const drawable = new Set<LaneId>(openOrder);
  const collapsedSet = new Set<LaneId>((opts.collapsed ?? []).filter((id) => drawable.has(id)));
  const hasChildren = new Set<LaneId>();
  const hiddenUnder = new Map<LaneId, number>();
  const folded = new Set<LaneId>();
  for (const id of openOrder) {
    // `guard` is the same hazard resolveRoots colours for: a corrupted originSession
    // cycle would otherwise walk this chain forever.
    const guard = new Set<LaneId>([id]);
    let hidden = false;
    for (let p = parentOf.get(id); p !== undefined && !guard.has(p); p = parentOf.get(p)) {
      guard.add(p);
      if (!drawable.has(p)) continue;
      hasChildren.add(p);
      if (collapsedSet.has(p)) {
        // Every collapsed ancestor counts it, not just the nearest: the outermost
        // one is swallowing that row too, and its "+3" has to say so.
        hiddenUnder.set(p, (hiddenUnder.get(p) ?? 0) + 1);
        hidden = true;
      }
    }
    if (hidden) folded.add(id);
  }
  const finalOrder = folded.size ? openOrder.filter((id) => !folded.has(id)) : openOrder;
  const rowOf = new Map<LaneId, number>(finalOrder.map((id, i) => [id, i]));
  const foldFields = (id: LaneId) => {
    const hidden = hiddenUnder.get(id) ?? 0;
    return {
      ...(hasChildren.has(id) ? { hasChildren: true as const } : {}),
      ...(hidden ? { hiddenDescendants: hidden } : {}),
    };
  };

  // 7. Lanes.
  const lanes: GraphLane[] = finalOrder.map((id) => {
    const root = rootInfos.get(id) ?? { rootId: id, depth: 0 };
    if (erasedIds.has(id)) {
      const lane: GraphLaneErased = {
        id,
        row: rowOf.get(id)!,
        rootId: root.rootId,
        depth: root.depth,
        presence: "gone",
        erased: true,
        label: id,
        runs: [],
        ...foldFields(id),
      };
      return lane;
    }
    const f = facts.get(id)!;
    const lane: GraphLaneKnown = {
      id,
      row: rowOf.get(id)!,
      rootId: root.rootId,
      depth: root.depth,
      presence: f.presence,
      label: f.label,
      kind: f.kind,
      origin: f.origin,
      runs: f.runs as [LaneRun, ...LaneRun[]],
      ...foldFields(id),
    };
    if (f.parent) lane.parent = f.parent;
    if (f.state !== undefined) lane.state = f.state;
    if (f.raw !== undefined) lane.raw = f.raw;
    return lane;
  });

  // 8. Segments — known/synthesized lanes only (erased lanes draw no band,
  // decision 6).
  const segments: GraphSegment[] = [];
  for (const id of finalOrder) {
    if (erasedIds.has(id)) continue;
    const f = facts.get(id)!;
    segments.push(...segmentsForLane(id, f.runs, f.presence, markersByLane.get(id) ?? [], from, to));
  }

  // 9. Arrows: spawn / fork / handoff off each included lane's birth, plus the
  // instruct / report / peer round trips — everything else is windowed to
  // [from, to] since these are point-in-time events on the timeline.
  interface ArrowCandidate {
    ts: number;
    variant: ArrowVariant;
    from: ActorId;
    to: ActorId;
    label?: string;
    danger?: boolean;
  }
  const convHistoryOf = new Map<LaneId, ConvSpan[]>();
  for (const id of knownIds) convHistoryOf.set(id, convHistory(byLane.get(id) ?? []));
  const resolveFork = (childId: LaneId, forkFrom: string, ts: number): LaneId | undefined => {
    for (const [id, history] of convHistoryOf) {
      if (id === childId) continue;
      if (convAt(history, ts) === forkFrom) return id;
    }
    return undefined;
  };

  const candidates: ArrowCandidate[] = [];
  for (const id of finalOrder) {
    if (erasedIds.has(id) || !knownIds.has(id)) continue;
    const birth = birthOf.get(id)!;
    if (birth.ts < from || birth.ts > to) continue;
    if (birth.originSession) {
      if (birth.origin === "session") candidates.push({ ts: birth.ts, variant: "spawn", from: birth.originSession, to: id });
      else if (birth.origin === "handoff") candidates.push({ ts: birth.ts, variant: "handoff", from: birth.originSession, to: id });
    }
    if (birth.forkFrom) {
      const source = resolveFork(id, birth.forkFrom, birth.ts);
      // Unresolved: forkFrom is a bare conversation id, and ActorId's external
      // vocabulary for one is "conv:<id>" — an unprefixed slug would read as
      // an (unknown) LANE id instead of the conversation it actually is.
      candidates.push({ ts: birth.ts, variant: "fork", from: source ?? `conv:${birth.forkFrom}`, to: id });
    }
  }
  for (const ev of page.activity) {
    if (ev.ts < from || ev.ts > to) continue;
    if (ev.ev === "instruct") {
      const c: ArrowCandidate = { ts: ev.ts, variant: "instruct", from: ev.from, to: ev.to };
      if (ev.excerpt !== undefined) c.label = ev.excerpt;
      candidates.push(c);
    } else if (ev.ev === "report") {
      const c: ArrowCandidate = { ts: ev.ts, variant: "report", from: ev.from, to: ev.to, danger: ev.reason !== undefined };
      if (ev.reason !== undefined) c.label = ev.reason;
      candidates.push(c);
    } else if (ev.ev === "peer") {
      const c: ArrowCandidate = { ts: ev.ts, variant: "peer", from: ev.from, to: ev.to };
      if (ev.excerpt !== undefined) c.label = ev.excerpt;
      candidates.push(c);
    }
  }
  // A ROUND-TRIP endpoint (instruct/report/peer) that names a real lane but
  // has no row THIS render — filtered out by showArchived/conversationId,
  // not deleted — must not draw as if it left the figure: that is the exact
  // misattribution decision 6 exists to prevent, just via a different filter
  // than deletion. Drop the round trip instead of lying about where it came
  // from. A LINEAGE arrow (spawn/fork/handoff) is the opposite case on
  // purpose: this graph's whole point is "who was born from whom", so a
  // parent with no row (window-excluded, not deleted) must still show the
  // edge — `fromRow: null` here is exactly decision 9's "欠けた親は左端の印
  // で示す", not decision 8-2's "left the figure". The variant tells the two
  // apart mechanically; `allLaneIds` also includes ancestor-only ids the
  // server sent purely for family-ordering context (never a row on their
  // own — see knownIds above), which is correct here too: such an ancestor
  // legitimately has no row this render either way.
  const allLaneIds = new Set<LaneId>([...knownIds, ...synthesizedIds, ...erasedIds]);
  const isFilteredOut = (id: string): boolean => allLaneIds.has(id) && !rowOf.has(id);
  const isRoundTrip = (v: ArrowVariant): boolean => v === "instruct" || v === "report" || v === "peer";
  const kept = candidates.filter((c) => !isRoundTrip(c.variant) || (!isFilteredOut(c.from) && !isFilteredOut(c.to)));
  const xs = jitterX(
    kept.map((c) => xOf(scale, c.ts)),
    width,
  );
  const rowFor = (id: string): number | null => (isExternalActor(id) ? null : (rowOf.get(id) ?? null));
  const arrows: GraphArrow[] = kept.map((c, i) => {
    const arrow: GraphArrow = {
      ts: c.ts,
      variant: c.variant,
      from: c.from,
      to: c.to,
      fromRow: rowFor(c.from),
      toRow: rowFor(c.to),
      x: xs[i],
    };
    if (c.label !== undefined) arrow.label = c.label;
    if (c.danger) arrow.danger = true;
    return arrow;
  });

  // 10. Coverage marks — where the 3-stage decay (decision 8) cuts off, drawn
  // only when the cut actually falls inside the visible window.
  const marks: CoverageMark[] = [];
  const addMark = (t: number | null | undefined, kind: CoverageMark["kind"]) => {
    if (t === null || t === undefined) return;
    if (t < from || t > to) return;
    marks.push({ t, kind, x: xOf(scale, t) });
  };
  addMark(page.coverage.activitySince, "activity-start");
  addMark(page.coverage.lineageSince, "lineage-start");
  addMark(page.coverage.backfilledBefore, "backfill-start");
  marks.sort((a, b) => a.t - b.t);

  return { scale, lanes, segments, arrows, marks, height: finalOrder.length * laneH };
};
