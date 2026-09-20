// FleetGraphView — the fleet session graph (ADR 0096): lanes are sessions, time runs
// left to right. Hand-written inline SVG over a pure layout model, the same structural
// template as the SCM commit graph (features/scm/CommitGraph.tsx + lib/gitgraph.ts).
//
// The render model (GraphModel) comes from lib/fleetgraph.ts's BuildFleetGraph in the
// finished feature. That module is S-LOGIC's (docs/log/101 §101.6) and has not landed
// yet — P1's three lanes run in parallel — so this view draws a FIXED FIXTURE model
// instead (./fixture.ts), exactly as docs/log/101 §101.6 allows: "S-LOGIC の関数が出来る
// までは fixture の GraphModel で描いてよい（P2 で差し替え）". `xOf`/`laneY` are a
// same-shape stand-in for S-LOGIC's own exports (./geometry.ts) for the same reason.
//
// GET /api/fleet-graph IS wired (core/api/client.ts fetchFleetGraph) so the network path
// exists end to end, but its response cannot become a GraphModel without BuildFleetGraph —
// P1 uses it only to surface a load error, and P2 integration is expected to replace the
// fixture call with BuildFleetGraph(page, sessions, opts) using the very state this
// already tracks.
import { useEffect, useMemo, useState } from "react";
import type { CSSProperties, ReactNode, WheelEvent as RWheelEvent } from "react";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { cx } from "../../ui/cx.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { useIsMobile } from "../../lib/device.ts";
import { fmtDateTime, TIME_HM } from "../../lib/intl.ts";
import { exitLabel } from "../../lib/sessionview.ts";
import { kindClass, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { openSessionFromList } from "../sessions/open.ts";
import { fetchFleetGraph } from "../../core/api/client.ts";
import type {
  ActorId,
  ArrowVariant,
  CoverageMark,
  GraphArrow,
  GraphLane,
  GraphLaneKnown,
  GraphModel,
  GraphScale,
  GraphSegment,
  LedgerState,
} from "../../types/fleetgraph.ts";
import type { SessionKind } from "../../types/session.ts";
import { buildFixtureGraph } from "./fixture.ts";
import { clampToScale, laneY, xOf } from "./geometry.ts";
import "./fleetgraph.css";

const ROW_H = 34;
const SEG_H = 12;
const NODE_R = 4;
const DEATH_R = 5;
const TOP_PAD = 26;
const BOTTOM_PAD = 22;
const LABEL_W = 176;
const CANVAS_W = 920;
const DAY_MS = 86_400_000;
const MIN_SPAN_MS = 30 * 60_000; // 30 minutes — zooming past this stops being readable
const MAX_SPAN_MS = 30 * DAY_MS; // matches decision 8's activity-retention tier

const STATE_KEY: Record<LedgerState, MsgKey> = {
  working: "fgraph.state.working",
  compacting: "fgraph.state.compacting",
  idle: "fgraph.state.idle",
  question: "fgraph.state.question",
  plan: "fgraph.state.plan",
  permission: "fgraph.state.permission",
  blocked: "fgraph.state.blocked",
  auth: "fgraph.state.auth",
  limited: "fgraph.state.limited",
  spend_limit: "fgraph.state.spend_limit",
  failed: "fgraph.state.failed",
  aborted: "fgraph.state.aborted",
  unknown: "fgraph.state.unknown",
};

const PRESENCE_KEY: Record<GraphLaneKnown["presence"], MsgKey> = {
  live: "fgraph.presence_live",
  stopped: "fgraph.presence_stopped",
  archived: "fgraph.presence_archived",
  gone: "fgraph.presence_gone",
};

const MARK_KEY: Record<CoverageMark["kind"], MsgKey> = {
  "activity-start": "fgraph.mark_activity_start",
  "lineage-start": "fgraph.mark_lineage_start",
  "backfill-start": "fgraph.mark_backfill_start",
};

type Tr = (key: MsgKey, vars?: Record<string, string | number>) => string;

// The subset of a mouse/keyboard event `activate` needs. A KeyboardEvent has no `button`
// (undefined reads as "not the middle button", correctly), so Enter/Space on a label and a
// click on the same label share one code path instead of a cast between event types.
interface ClickMods {
  preventDefault(): void;
  ctrlKey: boolean;
  metaKey: boolean;
  button?: number;
}

const ARROW_KEY: Record<ArrowVariant, MsgKey> = {
  spawn: "fgraph.arrow_spawn",
  fork: "fgraph.arrow_fork",
  handoff: "fgraph.arrow_handoff",
  instruct: "fgraph.arrow_instruct",
  report: "fgraph.arrow_report",
  peer: "fgraph.arrow_peer",
};

// "Family" edges (spawn/fork/handoff) connect lanes at birth; the other three are
// conversation-shaped round trips. Kept apart visually so a dense figure still reads at
// a glance which kind of line is which (docs/log/101 §101.4).
const isFamilyArrow = (v: ArrowVariant): boolean => v === "spawn" || v === "fork" || v === "handoff";

interface FleetGraphViewProps {
  paneId: string;
  showArchived: boolean;
  headerActions?: ReactNode;
}

export function FleetGraphView({ paneId, showArchived, headerActions }: FleetGraphViewProps) {
  const tr = useT();
  const beside = !useIsMobile();
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);

  const [win, setWin] = useState(() => {
    const to = Date.now();
    return { from: to - DAY_MS, to };
  });

  const zoom = (factor: number) => {
    setWin((w) => {
      const cur = w.to - w.from;
      const next = Math.min(MAX_SPAN_MS, Math.max(MIN_SPAN_MS, cur * factor));
      return { from: w.to - next, to: w.to };
    });
  };
  const pan = (fraction: number) => {
    setWin((w) => {
      const shift = (w.to - w.from) * fraction;
      const to = Math.min(Date.now(), w.to + shift);
      return { from: to - (w.to - w.from), to };
    });
  };
  const resetWindow = () => {
    const to = Date.now();
    setWin({ from: to - DAY_MS, to });
  };
  const onWheel = (e: RWheelEvent<HTMLDivElement>) => {
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault();
      zoom(e.deltaY > 0 ? 1.2 : 1 / 1.2);
      return;
    }
    const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
    if (!delta) return;
    e.preventDefault();
    pan(delta > 0 ? 0.12 : -0.12);
  };

  // GET /api/fleet-graph is wired end to end; P1 reads it only for a load-error signal
  // (the fixture below draws regardless — see the file header). No new poll: this fires
  // once per window change and on tab refocus (useRetryLoad's own rule), never on a timer.
  const [loadError, setLoadError] = useState(false);
  useEffect(() => {
    const ac = new AbortController();
    fetchFleetGraph(win.from, win.to)
      .then((res) => {
        if (ac.signal.aborted) return;
        setLoadError(!!(res && "error" in res));
      })
      .catch(() => {
        if (!ac.signal.aborted) setLoadError(true);
      });
    return () => ac.abort();
  }, [win.from, win.to]);

  const scale: GraphScale = useMemo(
    () => ({ from: win.from, to: win.to, width: CANVAS_W, laneH: ROW_H }),
    [win.from, win.to],
  );
  const fullModel = useMemo(() => buildFixtureGraph(scale), [scale]);
  const model: GraphModel = useMemo(() => {
    if (showArchived) return fullModel;
    const hidden = new Set(fullModel.lanes.filter((l) => !l.erased && l.presence === "archived").map((l) => l.id));
    if (hidden.size === 0) return fullModel;
    const lanes = fullModel.lanes.filter((l) => !hidden.has(l.id));
    return {
      ...fullModel,
      lanes,
      segments: fullModel.segments.filter((s) => !hidden.has(s.laneId)),
      arrows: fullModel.arrows.filter(
        (a) => !(a.fromRow != null && rowIsHidden(fullModel.lanes, hidden, a.fromRow)) &&
          !(a.toRow != null && rowIsHidden(fullModel.lanes, hidden, a.toRow)),
      ),
      // Recomputed below the fold too, but also needed here: an unfiltered height would
      // reserve space for the hidden rows at the BOTTOM of the figure instead of closing the
      // gap, once the rows themselves are compacted (visualRow, just below).
      height: lanes.length * scale.laneH,
    };
  }, [fullModel, showArchived, scale.laneH]);

  const laneById = useMemo(() => new Map(model.lanes.map((l) => [l.id, l] as const)), [model.lanes]);
  // The label column stacks whatever `model.lanes` holds, in array order — hiding archived
  // lanes (above) removes DOM rows from it, which compacts the gap away for free. The SVG
  // has no such free compaction: it positions everything by `lane.row`, the ORIGINAL index
  // from the full fixture, so a naive draw would leave the bars one row-slot below where the
  // (now-compacted) label points. `visualRow` is the same compaction applied to the numbers
  // the SVG draws with, keyed by lane id so segments and arrows (which only carry ids/absolute
  // rows, not array position) can look it up too.
  const visualRow = useMemo(() => new Map(model.lanes.map((l, i) => [l.id, i] as const)), [model.lanes]);

  const toggleArchived = () => setPaneTarget(paneId, { content: { kind: "fleetgraph", showArchived: !showArchived } });

  const running = useWorkspaceStore((s) => s.state === "running");
  const sessions = useSessionsStore((s) => s.sessions);
  const openLane = (laneId: string, newPane: boolean) => {
    const session = sessions.find((s) => s.name === laneId);
    if (!session) return; // pruned / never in this workspace's live list — nothing to attach to
    openSessionFromList(session, newPane, running);
  };
  const openConv = (conv: string, newPane: boolean) => {
    const target = { content: { kind: "chat" as const, conversationId: conv, draftAssistantId: null } };
    if (newPane) useLayoutStore.getState().openTargetInNew(target);
    else useLayoutStore.getState().openTarget(target);
  };
  // ADR 0078 decision 3, borrowed as-is (ADR 0096 decision 9): a plain click opens beside
  // when there is room, the same pane on a phone; Ctrl/⌘/middle always forces a new pane.
  const openActor = (actor: ActorId, newPane: boolean) => {
    if (actor.startsWith("conv:")) openConv(actor.slice(5), newPane || beside);
    else if (laneById.has(actor)) openLane(actor, newPane || beside);
    // user / schedule / bridge:* / agent have no pane to open.
  };
  const clickable = (actor: ActorId): boolean => actor.startsWith("conv:") || laneById.has(actor);

  const totalH = TOP_PAD + model.height + BOTTOM_PAD;
  const topY = (row: number) => TOP_PAD + laneY(scale, row);
  const ticks = useMemo(() => axisTicks(win.from, win.to), [win.from, win.to]);

  return (
    <div className="fgraph">
      <ViewHead
        actions={
          <>
            <span className="fgraph-nav">
              <button type="button" className="ui-btn ui-btn-ghost" title={tr("fgraph.pan_left")} onClick={() => pan(-0.25)}>
                <Icon name="chevron-left" />
              </button>
              <button type="button" className="ui-btn ui-btn-ghost" title={tr("fgraph.zoom_out")} onClick={() => zoom(2)}>
                <Icon name="zoom-out" />
              </button>
              <button type="button" className="ui-btn ui-btn-ghost" title={tr("fgraph.reset_window")} onClick={resetWindow}>
                <Icon name="history" />
              </button>
              <button type="button" className="ui-btn ui-btn-ghost" title={tr("fgraph.zoom_in")} onClick={() => zoom(0.5)}>
                <Icon name="zoom-in" />
              </button>
              <button type="button" className="ui-btn ui-btn-ghost" title={tr("fgraph.pan_right")} onClick={() => pan(0.25)}>
                <Icon name="chevron-right" />
              </button>
            </span>
            <button
              type="button"
              className={cx("ui-btn ui-btn-ghost fgraph-toggle", showArchived && "on")}
              aria-pressed={showArchived}
              title={tr("fgraph.show_archived_hint")}
              onClick={toggleArchived}
            >
              <Icon name={showArchived ? "eye" : "eye-closed"} /> <span className="lbl">{tr("fgraph.show_archived")}</span>
            </button>
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="graph" /> {tr("pane.kind.fleetgraph")}
        </span>
        <span className="fgraph-count">{tr("fgraph.count", { n: model.lanes.length })}</span>
        {loadError && <span className="fgraph-err" title={tr("err.network")}>{tr("fgraph.load_failed")}</span>}
      </ViewHead>
      {model.lanes.length === 0 ? (
        <EmptyState icon="graph" title={tr("fgraph.empty")} />
      ) : (
        <div className="fgraph-body" onWheel={onWheel}>
          <div className="fgraph-labels" style={{ width: LABEL_W, paddingTop: TOP_PAD }}>
            {model.lanes.map((lane) => (
              <LaneLabel key={lane.id} lane={lane} h={ROW_H} clickable={!lane.erased && clickable(lane.id)} onOpen={(e) => activate(e, (np) => openActor(lane.id, np))} />
            ))}
          </div>
          <div className="fgraph-canvas">
            <svg viewBox={`0 0 ${CANVAS_W} ${totalH}`} preserveAspectRatio="none" className="fgraph-svg" role="img" aria-label={tr("pane.kind.fleetgraph")}>
              <defs>
                {/* A marker's `color` inherits from where it sits in the DOM (inside <defs>), not
                    from the <line> that references it via marker-end — so a fixed neutral fill
                    here, not currentColor, which would silently pick up the SVG root's color
                    instead of the arrow's own category hue. */}
                <marker id="fgraph-arrowhead" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
                  <path d="M0,0 L8,4 L0,8 Z" fill="var(--muted)" />
                </marker>
                {/* "unknown" band fill — both sources of unknown (not-observed / observed but
                    unrecognised) share this pattern; the tooltip is what tells them apart. */}
                <pattern id="fgraph-hatch" width={6} height={6} patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
                  <line x1={0} y1={0} x2={0} y2={6} stroke="var(--muted2)" strokeWidth={3} />
                </pattern>
              </defs>

              {model.marks.map((m, i) => <CoverageLine key={i} mark={m} totalH={totalH} label={tr(MARK_KEY[m.kind])} />)}

              {model.lanes.map((lane) => (
                <LaneLine key={lane.id} lane={lane} scale={scale} y={topY(visualRow.get(lane.id) ?? lane.row)} onOpen={(e) => !lane.erased && clickable(lane.id) && activate(e, (np) => openActor(lane.id, np))} />
              ))}

              {model.segments.map((seg, i) => {
                const owner = laneById.get(seg.laneId);
                const kind = owner && !owner.erased ? owner.kind : undefined;
                return <SegmentRect key={i} seg={seg} scale={scale} y={topY(visualRow.get(seg.laneId) ?? 0)} kind={kind} tr={tr} />;
              })}

              {model.arrows.map((a, i) => {
                // Same compaction as the lanes: an arrow's fromRow/toRow are absolute indices
                // baked in against the FULL fixture, so they need the same id->visualRow
                // translation the lane lines just got. null (an off-figure end) passes through.
                const fromRow = a.fromRow == null ? null : (visualRow.get(a.from) ?? null);
                const toRow = a.toRow == null ? null : (visualRow.get(a.to) ?? null);
                return (
                  <ArrowGlyph
                    key={i}
                    arrow={{ ...a, fromRow, toRow }}
                    topY={topY}
                    tr={tr}
                    clickable={clickable(a.from) || clickable(a.to)}
                    onOpen={(e) => activate(e, (np) => openActor(clickable(a.to) ? a.to : a.from, np))}
                  />
                );
              })}

              {ticks.map((tk) => (
                <g key={tk.ts} className="fgraph-tick">
                  <line x1={xOf(scale, tk.ts)} x2={xOf(scale, tk.ts)} y1={TOP_PAD - 4} y2={totalH - BOTTOM_PAD + 4} />
                  <text x={xOf(scale, tk.ts)} y={totalH - 6}>{tk.label}</text>
                </g>
              ))}
            </svg>
          </div>
        </div>
      )}
    </div>
  );
}

function rowIsHidden(lanes: GraphLane[], hidden: Set<string>, row: number): boolean {
  const lane = lanes.find((l) => l.row === row);
  return !!lane && hidden.has(lane.id);
}

// Ctrl/⌘/middle click always forces a new pane; a plain click leaves it to the caller
// (which folds in `beside`). Mirrors SessionCard's openIt (features/overview).
function activate(e: ClickMods, run: (newPane: boolean) => void): void {
  e.preventDefault();
  run(e.ctrlKey || e.metaKey || e.button === 1);
}

function axisTicks(from: number, to: number): { ts: number; label: string }[] {
  const span = to - from;
  const steps = 5;
  const out: { ts: number; label: string }[] = [];
  for (let i = 0; i <= steps; i++) {
    const ts = from + (span * i) / steps;
    out.push({ ts, label: fmtDateTime(ts, span > 3 * DAY_MS ? undefined : TIME_HM) });
  }
  return out;
}

function LaneLabel({ lane, h, clickable, onOpen }: { lane: GraphLane; h: number; clickable: boolean; onOpen: (e: ClickMods) => void }) {
  const tr = useT();
  if (lane.erased) {
    return (
      <div className="fgraph-label erased" style={{ height: h }} title={tr("fgraph.erased_label", { id: lane.label })}>
        <Icon name="trash" />
        <span className="fgraph-label-text">{tr("fgraph.erased_label", { id: lane.label })}</span>
      </div>
    );
  }
  const cls = kindClass(lane.kind);
  return (
    <div
      className={cx("fgraph-label", clickable && "clickable", lane.depth > 0 && "child")}
      style={{ height: h, paddingLeft: 6 + Math.min(lane.depth, 4) * 10 }}
      role={clickable ? "button" : undefined}
      tabIndex={clickable ? 0 : undefined}
      title={`${lane.label}\n${kindLabel(lane.kind)} · ${tr(PRESENCE_KEY[lane.presence])}`}
      onClick={clickable ? onOpen : undefined}
      onAuxClick={clickable ? onOpen : undefined}
      onKeyDown={
        clickable
          ? (e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onOpen(e);
              }
            }
          : undefined
      }
    >
      <span className={"sess-kic kind-" + cls}>
        <Icon name={kindIcon(lane.kind)} />
      </span>
      <span className="fgraph-label-text">{lane.label}</span>
    </div>
  );
}

function CoverageLine({ mark, totalH, label }: { mark: CoverageMark; totalH: number; label: string }) {
  return (
    <g className={"fgraph-mark " + mark.kind}>
      <line x1={mark.x} x2={mark.x} y1={TOP_PAD - 8} y2={totalH - BOTTOM_PAD + 8} />
      <text x={mark.x + 4} y={TOP_PAD - 10}>{label}</text>
    </g>
  );
}

function LaneLine({ lane, scale, y, onOpen }: { lane: GraphLane; scale: GraphScale; y: number; onOpen: (e: ClickMods) => void }) {
  const tr = useT();
  if (lane.erased) return null; // ADR 0096 decision 6: an erased lane is a row of arrows, no line at all.
  const nodes: ReactNode[] = [];
  lane.runs.forEach((run, i) => {
    const x0 = xOf(scale, clampToScale(scale, run.t0));
    const open = run.t1 == null;
    const x1 = xOf(scale, clampToScale(scale, run.t1 ?? scale.to));
    nodes.push(<line key={`r${i}`} className="fgraph-run" x1={x0} x2={x1} y1={y} y2={y} />);
    // ○ at every run's start — birth for the first run, revive for any later one (the
    // ADR's own ASCII reuses the same glyph for both: "○──×──○──×").
    nodes.push(<circle key={`b${i}`} className="fgraph-birth" cx={x0} cy={y} r={NODE_R} />);
    if (!open) {
      const isLast = i === lane.runs.length - 1;
      const ex = exitLabel({ exitReason: run.exitReason, exitCode: run.exitCode, exitSignal: run.exitSignal });
      const title = run.cut
        ? tr("fgraph.run_cut", { time: fmtDateTime(run.t1!, TIME_HM) })
        : tr("fgraph.death_at", { time: fmtDateTime(run.t1!, TIME_HM) }) + (ex ? "\n" + ex.hint : "");
      nodes.push(
        <g key={`d${i}`} className={cx("fgraph-death", run.cut && "cut", ex && "danger")} transform={`translate(${x1},${y})`}>
          <title>{title}</title>
          {run.cut ? <circle r={DEATH_R} /> : null}
          <line x1={-DEATH_R} y1={-DEATH_R} x2={DEATH_R} y2={DEATH_R} />
          <line x1={-DEATH_R} y1={DEATH_R} x2={DEATH_R} y2={-DEATH_R} />
        </g>,
      );
      // Only the LAST run's tail says whether the lane is still reachable (decision 12):
      // stopped → dashed to the right edge, archived → faintly dashed, gone → nothing.
      if (isLast && !run.cut) {
        const tailX = xOf(scale, scale.to);
        if (lane.presence === "stopped") nodes.push(<line key="tail" className="fgraph-tail stopped" x1={x1} x2={tailX} y1={y} y2={y} />);
        else if (lane.presence === "archived") nodes.push(<line key="tail" className="fgraph-tail archived" x1={x1} x2={tailX} y1={y} y2={y} />);
      }
    } else if (lane.presence === "live") {
      // Right-edge pulse: the lane is running RIGHT NOW.
      nodes.push(<circle key="pulse" className="fgraph-pulse" cx={x1} cy={y} r={NODE_R + 1} />);
    }
  });
  // One CSS custom property carries the kind color down to every child rule below
  // (.fgraph-run/.fgraph-birth/…), instead of a `.kind-<slug>` class repeated per element —
  // reusing the SAME --kind-* variables the rail and the session cards already read, so
  // there is no new per-kind CSS to keep in sync (memo `kind-color-css-checklist`).
  const laneColor = { "--lane-color": `var(--kind-${kindClass(lane.kind)})` } as CSSProperties;
  return (
    <g className="fgraph-lane-hit" onClick={onOpen} onAuxClick={onOpen} style={laneColor}>
      {/* A wide, invisible hit target under the (thin) drawn line so the row is easy to click. */}
      <line x1={0} x2={scale.width} y1={y} y2={y} className="fgraph-hit" />
      {nodes}
    </g>
  );
}

function SegmentRect({ seg, scale, y, kind, tr }: { seg: GraphSegment; scale: GraphScale; y: number; kind: SessionKind | undefined; tr: Tr }) {
  const x0 = xOf(scale, clampToScale(scale, seg.t0));
  const x1 = xOf(scale, clampToScale(scale, seg.t1));
  const w = Math.max(1, x1 - x0);
  const stateWord = seg.state ? tr(STATE_KEY[seg.state]) : null;
  // The TWO sources of "unknown" (contract comment on GraphSegment): tell them apart by
  // whether `state` is set at all, never draw "not observed" over a stretch that WAS.
  const title =
    seg.kind === "unknown"
      ? seg.state === "unknown"
        ? tr("fgraph.seg_unrecognized", { raw: seg.raw ?? "?" })
        : tr("fgraph.seg_not_observed")
      : stateWord ?? seg.kind;
  // Only the "active" band carries the kind color (which agent is doing the work); the
  // other bands are neutral by state (idle/waiting/unknown), same as the rail's chips.
  const style = seg.kind === "active" && kind ? ({ "--seg-color": `var(--kind-${kindClass(kind)})` } as CSSProperties) : undefined;
  return (
    <rect className={cx("fgraph-seg", seg.kind)} x={x0} y={y - SEG_H / 2} width={w} height={SEG_H} rx={2} style={style}>
      <title>{title}</title>
    </rect>
  );
}

function ArrowGlyph({
  arrow,
  topY,
  tr,
  clickable,
  onOpen,
}: {
  arrow: GraphArrow;
  topY: (row: number) => number;
  tr: Tr;
  clickable: boolean;
  onOpen: (e: ClickMods) => void;
}) {
  const EDGE_Y = TOP_PAD - 14;
  const y0 = arrow.fromRow == null ? EDGE_Y : topY(arrow.fromRow);
  const y1 = arrow.toRow == null ? EDGE_Y : topY(arrow.toRow);
  const variantCls = isFamilyArrow(arrow.variant) ? "family" : arrow.variant;
  const title =
    tr(ARROW_KEY[arrow.variant], arrow.variant === "peer" ? { intent: arrow.label ?? "" } : {}) +
    (arrow.variant !== "peer" && arrow.label ? "\n" + arrow.label : "") +
    (arrow.danger ? "\n" + tr("fgraph.death_reason", { reason: arrow.label ?? "" }) : "");
  return (
    <g
      className={cx("fgraph-arrow", variantCls, arrow.danger && "danger", clickable && "clickable")}
      onClick={clickable ? onOpen : undefined}
      onAuxClick={clickable ? onOpen : undefined}
    >
      <title>{title}</title>
      <line x1={arrow.x} x2={arrow.x} y1={y0} y2={y1} markerEnd="url(#fgraph-arrowhead)" />
      {(arrow.fromRow == null || arrow.toRow == null) && <circle className="fgraph-edge-pt" cx={arrow.x} cy={EDGE_Y} r={2.5} />}
    </g>
  );
}
