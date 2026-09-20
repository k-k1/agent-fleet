// FleetGraphView — the fleet session graph (ADR 0096): lanes are sessions, time runs
// left to right. Hand-written inline SVG over a pure layout model, the same structural
// template as the SCM commit graph (features/scm/CommitGraph.tsx + lib/gitgraph.ts).
//
// The render model comes from lib/fleetgraph.ts's buildFleetGraph, which merges the
// served ledger page (history) with the live sessions map (what is true now) — a lane's
// presence needs both, so this view holds the page and hands both over rather than
// deriving anything itself. Geometry (xOf/laneY) comes from there too: a second copy
// here once drifted by half a row and neither tsc nor the tests could see it, because a
// module importing its own stub looks unrelated to the real one.
import { useEffect, useMemo, useRef, useState } from "react";
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
  FleetGraphPage,
  GraphArrow,
  GraphLane,
  GraphLaneKnown,
  GraphModel,
  GraphScale,
  GraphSegment,
  LedgerState,
} from "../../types/fleetgraph.ts";
import type { SessionKind } from "../../types/session.ts";
import { buildFleetGraph, laneY, xOf } from "../../lib/fleetgraph.ts";
import "./fleetgraph.css";

const ROW_H = 34;
const SEG_H = 12;
const NODE_R = 4;
const DEATH_R = 5;
const TOP_PAD = 26;
const BOTTOM_PAD = 22;
const LABEL_W = 176;
// Fallback only, used until the canvas's ResizeObserver reports a real width (and in the
// dom test project, whose ResizeObserver stub never calls back at all — see domSetup.ts).
const CANVAS_W = 920;
const MIN_CANVAS_W = 240;
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

// The actor spellings decision 8-2 says never become a lane. A family arrow's `from` is
// contractually always a session (a lane id), so `fromRow == null` on one of THESE means
// something different from `fromRow == null` on a round trip: not "this end has no lane at
// all", but "this lane exists, just not drawn here" (decision 9's missing-parent case,
// off-window or deleted lineage) — see the arrows.map() below.
const isNonLaneActor = (actor: ActorId): boolean =>
  actor.startsWith("conv:") || actor === "user" || actor === "schedule" || actor === "agent" || actor.startsWith("bridge:");

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

  // One fetch per window change (and on tab refocus), never on a timer: the figure rides
  // the session list's existing polling for "now" and re-reads history only when the window
  // moves. A failed load keeps the previous page on screen rather than blanking the figure.
  const [loadError, setLoadError] = useState(false);
  const [page, setPage] = useState<FleetGraphPage | null>(null);
  useEffect(() => {
    const ac = new AbortController();
    fetchFleetGraph(win.from, win.to)
      .then((res) => {
        if (ac.signal.aborted) return;
        const failed = !!(res && "error" in res);
        setLoadError(failed);
        if (!failed) setPage(res as FleetGraphPage);
      })
      .catch(() => {
        if (!ac.signal.aborted) setLoadError(true);
      });
    return () => ac.abort();
  }, [win.from, win.to]);

  // The canvas's real width, not a constant: `.fgraph-svg` now carries explicit width/
  // height attributes matching `scale` 1:1 (px-for-px with the label column's fixed
  // 34px rows), rather than a percentage width scaled by the browser against a viewBox —
  // that scaled the SVG's internal geometry by containerWidth/920 while the label column
  // next to it stayed at a literal 34px, so the two drifted apart by row (measured: ~17px
  // per row at 1700px wide, ~160px by row 6).
  const sessions = useSessionsStore((s) => s.sessions);
  const canvasRef = useRef<HTMLDivElement>(null);
  const [canvasW, setCanvasW] = useState(CANVAS_W);
  useEffect(() => {
    const el = canvasRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w && w > 0) setCanvasW(Math.round(w));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const scale: GraphScale = useMemo(
    () => ({ from: win.from, to: win.to, width: Math.max(MIN_CANVAS_W, canvasW), laneH: ROW_H }),
    [win.from, win.to, canvasW],
  );
  const sessionMap = useMemo(() => new Map(sessions.map((x) => [x.name, x] as const)), [sessions]);
  const model: GraphModel = useMemo(
    () => buildFleetGraph(page, sessionMap, {
      from: scale.from,
      to: scale.to,
      width: scale.width,
      laneH: scale.laneH,
      showArchived,
    }),
    [page, sessionMap, scale, showArchived],
  );

  const laneById = useMemo(() => new Map(model.lanes.map((l) => [l.id, l] as const)), [model.lanes]);
  const toggleArchived = () => setPaneTarget(paneId, { content: { kind: "fleetgraph", showArchived: !showArchived } });

  const running = useWorkspaceStore((s) => s.state === "running");
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
  // laneY returns the row's TOP (matching S-LOGIC's own formula, confirmed in review);
  // every line/arrow this view draws wants the midline, so the +laneH/2 lives here, once.
  const topY = (row: number) => TOP_PAD + laneY(scale, row) + scale.laneH / 2;
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
            {model.lanes.map((lane) => {
              // "欠けた親は左端の印で示す" (decision 9): a lane can sit at depth > 0 while its
              // immediate parent has no row of its own in this model — off-window, or the
              // parent's OWN lineage was deleted (docs/log/101 §101.8). Either way, indenting
              // silently would read as "this session has no history", which is the one thing
              // depth is there to deny.
              // Name the absent parent rather than only flagging it: the whole point of
              // keeping `parent`/`rootId` for an off-window ancestor (decision 9) is that a
              // family stays readable, and "some parent exists" does not carry that.
              const missingParent = !lane.erased && lane.depth > 0 && lane.parent && !laneById.has(lane.parent) ? lane.parent : null;
              return (
                <LaneLabel
                  key={lane.id}
                  lane={lane}
                  h={ROW_H}
                  clickable={!lane.erased && clickable(lane.id)}
                  missingParent={missingParent}
                  onOpen={(e) => activate(e, (np) => openActor(lane.id, np))}
                />
              );
            })}
          </div>
          <div className="fgraph-canvas" ref={canvasRef}>
            <svg
              width={scale.width}
              height={totalH}
              viewBox={`0 0 ${scale.width} ${totalH}`}
              className="fgraph-svg"
              role="img"
              aria-label={tr("pane.kind.fleetgraph")}
            >
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
                <LaneLine key={lane.id} lane={lane} scale={scale} y={topY(lane.row)} onOpen={(e) => !lane.erased && clickable(lane.id) && activate(e, (np) => openActor(lane.id, np))} />
              ))}

              {model.segments.map((seg) => {
                const row = laneById.get(seg.laneId)?.row;
                // A segment whose lane isn't in this model should not happen (segments never
                // outlive their lane), but "guess row 0" would silently paint another,
                // unrelated session's band — skip instead of fabricating data nobody observed.
                if (row === undefined) return null;
                const owner = laneById.get(seg.laneId);
                const kind = owner && !owner.erased ? owner.kind : undefined;
                return <SegmentRect key={`${seg.laneId}-${seg.t0}-${seg.t1}`} seg={seg} scale={scale} y={topY(row)} kind={kind} tr={tr} />;
              })}

              {model.arrows.map((a) => {
                const fromRow = a.fromRow;
                const toRow = a.toRow;
                // decision 8-2's "leaves the figure" glyph is for actors that never have a
                // lane at all (conv:/user/schedule/bridge:*/agent) — a family edge's ends are
                // ALWAYS sessions, so a null row there means decision 9's "missing parent"
                // instead: the lane exists, just isn't drawn in this window. That case already
                // gets its own mark next to the child's label (LaneLabel's .fgraph-parent-gap,
                // driven independently off `lane.parent`) — drawing a SECOND, different-looking
                // mark here would misattribute the birth to an outside actor AND duplicate the
                // one the label already shows, so this arrow draws nothing at all.
                const missingParent =
                  isFamilyArrow(a.variant) &&
                  ((fromRow == null && !isNonLaneActor(a.from)) || (toRow == null && !isNonLaneActor(a.to)));
                if (missingParent) return null;
                return (
                  <ArrowGlyph
                    key={`${a.ts}-${a.variant}-${a.from}-${a.to}`}
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

function LaneLabel({
  lane,
  h,
  clickable,
  missingParent,
  onOpen,
}: {
  lane: GraphLane;
  h: number;
  clickable: boolean;
  missingParent: string | null;
  onOpen: (e: ClickMods) => void;
}) {
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
            {missingParent && (
              <Icon name="warning" className="fgraph-parent-gap" title={tr("fgraph.parent_missing", { id: missingParent })} />
            )}
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
    const x0 = xOf(scale, run.t0); // xOf clamps to the window itself
    const open = run.t1 == null;
    const x1 = xOf(scale, run.t1 ?? scale.to);
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
      } else if (isLast && run.cut) {
        // "その先は不明" (decision 12): a cut run draws NOTHING like a stopped/archived tail
        // would (that reads as "resumable", which this lane is not confirmed to be) — instead
        // the stretch after the hollow × is hatched exactly like an "unknown" activity band,
        // so it reads as uncertain rather than either "still there" or "definitely gone".
        const tailX = xOf(scale, scale.to);
        nodes.push(
          <rect
            key="cut-tail"
            className="fgraph-cut-unknown"
            x={x1}
            y={y - SEG_H / 2}
            width={Math.max(1, tailX - x1)}
            height={SEG_H}
            rx={2}
          >
            <title>{tr("fgraph.seg_not_observed")}</title>
          </rect>,
        );
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
  const x0 = xOf(scale, seg.t0); // xOf clamps to the window itself
  const x1 = xOf(scale, seg.t1);
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
