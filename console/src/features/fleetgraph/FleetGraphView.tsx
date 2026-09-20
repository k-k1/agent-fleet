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
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  CSSProperties,
  MouseEvent as RMouseEvent,
  PointerEvent as RPointerEvent,
  ReactNode,
  WheelEvent as RWheelEvent,
} from "react";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { cx } from "../../ui/cx.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { useIsMobile } from "../../lib/device.ts";
import { fmtDateTime, TIME_HM } from "../../lib/intl.ts";
import { exitLabel, stateInfo } from "../../lib/sessionview.ts";
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
import type { Session, SessionKind } from "../../types/session.ts";
import { buildFleetGraph, isExternalActor, laneY, xOf } from "../../lib/fleetgraph.ts";
import "./fleetgraph.css";

const ROW_H = 34;
const SEG_H = 12;
const NODE_R = 4;
const DEATH_R = 5;
const TOP_PAD = 26;
// Only the arrows that leave the figure and the coverage captions live above/below the
// rows now: the time axis is its own sticky strip on top (see AXIS_H), not the foot of
// the canvas — with more lanes than fit, a scale drawn at the bottom is scrolled out of
// sight exactly when the figure is big enough to need it.
const BOTTOM_PAD = 10;
const AXIS_H = 20;
// Wide enough for the name AND the state chip: at 248 the chip (67px measured) left 120px
// for the name and clipped 4 of the fixture's 9 lanes; at 288 none of them clip.
// (console/scripts/fleetgraph/check.mjs prints both numbers.)
const LABEL_W = 288;
// Narrow panes give the width back to the name: the state chip folds to its icon (CSS)
// and the column shrinks with it. A container query cannot do this half — the SVG's
// width is a number in the model, not a style.
const LABEL_W_NARROW = 168;
const LABEL_W_BREAK = 560;
// Fallback only, used until the body's ResizeObserver reports a real width (and in the
// dom test project, whose ResizeObserver stub never calls back at all — see domSetup.ts).
const CANVAS_W = 920;
const MIN_CANVAS_W = 240;
const DAY_MS = 86_400_000;
const MIN_SPAN_MS = 30 * 60_000; // 30 minutes — zooming past this stops being readable
const MAX_SPAN_MS = 30 * DAY_MS; // matches decision 8's activity-retention tier
// A pan/zoom gesture moves the window on every wheel notch and every pointer move; the
// page it needs is re-fetched only once the gesture settles. Without this a single
// trackpad flick fired one request per frame (and each one re-rendered the figure under
// the finger). The FIRST load is not delayed: `fetchWin` starts equal to `win`, and the
// debounce below returns the SAME object when nothing moved, so React bails out.
const FETCH_SETTLE_MS = 250;
// How far a press may travel and still count as a click on what is underneath (a lane,
// an arrow). Beyond it the gesture was a pan, and the click it would synthesize is
// swallowed — otherwise dragging the canvas opens whatever the finger came down on.
const DRAG_SLOP_PX = 4;

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

// The same four words as PRESENCE_KEY, cut to chip length. Only reachable for a lane the
// live list does not carry — an archived one (the Agent's list skips `m.Archived`) or a
// deleted one — so it never competes with stateInfo's wording for a session that exists.
const PRESENCE_SHORT_KEY: Record<GraphLaneKnown["presence"], MsgKey> = {
  live: "fgraph.presence_short_live",
  stopped: "fgraph.presence_short_stopped",
  archived: "fgraph.presence_short_archived",
  gone: "fgraph.presence_short_gone",
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
  collapsed: string[];
  headerActions?: ReactNode;
}

export function FleetGraphView({ paneId, showArchived, collapsed, headerActions }: FleetGraphViewProps) {
  const tr = useT();
  const beside = !useIsMobile();
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);

  const [win, setWin] = useState(() => {
    const to = Date.now();
    return { from: to - DAY_MS, to };
  });
  // Both gestures keep the same two invariants: the span stays inside
  // [MIN_SPAN_MS, MAX_SPAN_MS], and the right edge never passes now — panning into the
  // future scrolls into a blank the figure can never fill.
  const zoom = (factor: number) => setWin((w) => {
    const span = Math.min(MAX_SPAN_MS, Math.max(MIN_SPAN_MS, (w.to - w.from) * factor));
    return { from: w.to - span, to: w.to };
  });
  const pan = (fraction: number) => setWin((w) => {
    const span = w.to - w.from;
    const to = Math.min(Date.now(), w.to + span * fraction);
    return { from: to - span, to };
  });
  const resetWindow = () => {
    const to = Date.now();
    setWin({ from: to - DAY_MS, to });
  };

  // The canvas's real width, not a constant: `.fgraph-svg` now carries explicit width/
  // height attributes matching `scale` 1:1 (px-for-px with the label column's fixed
  // 34px rows), rather than a percentage width scaled by the browser against a viewBox —
  // that scaled the SVG's internal geometry by containerWidth/920 while the label column
  // next to it stayed at a literal 34px, so the two drifted apart by row (measured: ~17px
  // per row at 1700px wide, ~160px by row 6).
  const sessions = useSessionsStore((s) => s.sessions);
  // Measure the SCROLL BOX, not the canvas: `.fgraph-body` scrolls (overflow:auto), so the
  // canvas inside it is sized by its own content — the SVG — and observing that is a loop
  // that latches at whatever width the first render used (measured: 920px at every viewport,
  // so the figure never widened on a 1700px pane and overflowed a 480px one). The body's
  // client width minus the label column is the width actually available; below MIN_CANVAS_W
  // the body scrolls horizontally instead of squeezing the time axis flat.
  //
  // A ref CALLBACK rather than useEffect + useRef: the body does not exist on the first
  // render (an empty figure renders EmptyState instead), so a mount-time effect would run
  // with a null ref and never observe anything — measured as a figure stuck at 920px wide
  // no matter the pane. The callback re-runs on every mount and unmount of the node itself.
  const [bodyW, setBodyW] = useState(CANVAS_W + LABEL_W);
  const roRef = useRef<ResizeObserver | null>(null);
  const bodyRef = useCallback((el: HTMLDivElement | null) => {
    roRef.current?.disconnect();
    roRef.current = null;
    if (!el) return;
    const measure = () => setBodyW(el.clientWidth);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    roRef.current = ro;
  }, []);
  const labelW = bodyW < LABEL_W_BREAK ? LABEL_W_NARROW : LABEL_W;
  const canvasW = Math.max(MIN_CANVAS_W, Math.round(bodyW - labelW));

  const scale: GraphScale = useMemo(
    () => ({ from: win.from, to: win.to, width: canvasW, laneH: ROW_H }),
    [win.from, win.to, canvasW],
  );

  // A gesture's px become a window shift through the scale itself: a wheel notch and a
  // drag both move the figure by exactly the distance the input travelled. The first
  // version panned a fixed 12% of the window per wheel EVENT, and one trackpad flick
  // (dozens of events) threw the window days away.
  const panPx = (px: number) => setWin((w) => {
    const span = w.to - w.from;
    const to = Math.min(Date.now(), w.to + px * (span / Math.max(1, canvasW)));
    return { from: to - span, to };
  });
  const onWheel = (e: RWheelEvent<HTMLDivElement>) => {
    if (e.ctrlKey || e.metaKey) {
      e.preventDefault();
      zoom(e.deltaY > 0 ? 1.2 : 1 / 1.2);
      return;
    }
    // A plain vertical wheel belongs to the LANE LIST, which is what actually overflows
    // this pane: swallowing it to pan time (what the first version did for every wheel
    // event, deltaY included) left no way at all to reach the rows below the fold.
    // Shift+wheel is the horizontal gesture a mouse without a tilt wheel can produce.
    const dx = e.shiftKey && !e.deltaX ? e.deltaY : e.deltaX;
    if (!dx) return;
    e.preventDefault();
    // A wheel scrolls the VIEWPORT: deltaX > 0 means "further right", i.e. later. The
    // opposite of a drag, which moves the CONTENT under the finger — measured with the
    // sign the wrong way round, where a leftward flick only pressed the window against
    // "now" and looked like the gesture did nothing at all.
    panPx(dx);
  };

  // Drag to pan, the gesture a timeline is expected to have. `moved` is what tells a pan
  // from a click on a lane: past DRAG_SLOP_PX the click that the browser synthesizes at
  // the end is swallowed in the capture phase, before any lane/arrow handler sees it.
  const dragRef = useRef<{ id: number; x: number; moved: boolean } | null>(null);
  const swallowClickRef = useRef(false);
  const onPointerDown = (e: RPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    dragRef.current = { id: e.pointerId, x: e.clientX, moved: false };
  };
  const onPointerMove = (e: RPointerEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    if (!d || d.id !== e.pointerId) return;
    const dx = e.clientX - d.x;
    if (!d.moved && Math.abs(dx) < DRAG_SLOP_PX) return;
    if (!d.moved) {
      d.moved = true;
      // Captured only once the press IS a drag: capturing on pointerdown would steal the
      // pointer from every plain click on a lane.
      e.currentTarget.setPointerCapture(e.pointerId);
    }
    d.x = e.clientX;
    panPx(-dx);
  };
  const endDrag = (e: RPointerEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    if (!d || d.id !== e.pointerId) return;
    dragRef.current = null;
    if (!d.moved) return;
    swallowClickRef.current = true;
    if (e.currentTarget.hasPointerCapture(e.pointerId)) e.currentTarget.releasePointerCapture(e.pointerId);
  };
  const onClickCapture = (e: RMouseEvent<HTMLDivElement>) => {
    if (!swallowClickRef.current) return;
    swallowClickRef.current = false;
    e.stopPropagation();
    e.preventDefault();
  };

  // One fetch per settled window (and on tab refocus), never on a timer: the figure rides
  // the session list's existing polling for "now" and re-reads history only when the window
  // moves. A failed load keeps the previous page on screen rather than blanking the figure.
  const [loadError, setLoadError] = useState(false);
  const [page, setPage] = useState<FleetGraphPage | null>(null);
  const [fetchWin, setFetchWin] = useState(win);
  useEffect(() => {
    const id = setTimeout(
      () => setFetchWin((w) => (w.from === win.from && w.to === win.to ? w : { from: win.from, to: win.to })),
      FETCH_SETTLE_MS,
    );
    return () => clearTimeout(id);
  }, [win.from, win.to]);
  useEffect(() => {
    const ac = new AbortController();
    fetchFleetGraph(fetchWin.from, fetchWin.to)
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
  }, [fetchWin.from, fetchWin.to]);

  const sessionMap = useMemo(() => new Map(sessions.map((x) => [x.name, x] as const)), [sessions]);
  // The fold list is an array on PaneContent (JSON in the layout), so it is a NEW array on
  // every render of the pane: keying the memo on its contents instead of its identity is
  // what keeps a rebuild of the whole model off every unrelated re-render.
  const collapsedKey = collapsed.join("\u0000");
  const model: GraphModel = useMemo(
    () => buildFleetGraph(page, sessionMap, {
      from: scale.from,
      to: scale.to,
      width: scale.width,
      laneH: scale.laneH,
      showArchived,
      collapsed: collapsedKey ? collapsedKey.split("\u0000") : [],
    }),
    [page, sessionMap, scale, showArchived, collapsedKey],
  );

  const laneById = useMemo(() => new Map(model.lanes.map((l) => [l.id, l] as const)), [model.lanes]);
  const setContent = (next: { showArchived?: boolean; collapsed?: string[] }) =>
    setPaneTarget(paneId, {
      content: {
        kind: "fleetgraph",
        showArchived: next.showArchived ?? showArchived,
        collapsed: next.collapsed ?? collapsed,
      },
    });
  const toggleArchived = () => setContent({ showArchived: !showArchived });
  const toggleFold = (laneId: string) =>
    setContent({
      collapsed: collapsed.includes(laneId) ? collapsed.filter((x) => x !== laneId) : [...collapsed, laneId],
    });
  // The sessions overview and this figure are two views of one surface (ADR 0096
  // decision 10), so the default is a swap in place — Ctrl/⌘/middle still means "a new
  // pane", the same modifier rule every other open in this view follows.
  const openSessionsList = (newPane: boolean) => {
    const target = { content: { kind: "sessions" as const, showStopped: false, collapsed } };
    if (newPane) useLayoutStore.getState().openTargetInNew(target);
    else setPaneTarget(paneId, target);
  };

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
            {/* The way back to the sessions overview (ADR 0096 decision 10). The two panes
                carry each other's button rather than only the leader key and the palette,
                which is all this figure had: the rail's layout map hides itself while there
                is a single pane — which is exactly when someone reaches for either view. */}
            <button
              type="button"
              className="ui-btn ui-btn-ghost fgraph-switch"
              title={tr("fgraph.switch_to_sessions_hint")}
              onClick={(e) => activate(e, openSessionsList)}
              onAuxClick={(e) => activate(e, openSessionsList)}
            >
              <Icon name="dashboard" /> <span className="lbl">{tr("fgraph.switch_to_sessions")}</span>
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
      {/* The body is rendered even with no lanes in it. An empty window is a PLACE — you
          reach it by panning into a stretch where nothing ran — and replacing the whole
          figure with a bare EmptyState took the time axis away (so you could not tell where
          you were) along with the wheel and drag handlers (so the only way back was the
          header's reset button). Measured: one flick into the past left a pane whose only
          working control was "reset". */}
      <div className="fgraph-body" ref={bodyRef} onWheel={onWheel}>
        {/* The time axis, in its own strip pinned to the TOP of the scroll box. It used to
            be the last thing inside the canvas, which put it below every lane: past a
            screenful of sessions the scale was only readable after scrolling to the
            bottom, and it moved away again the moment you looked at a row. */}
        <div className="fgraph-axis" style={{ height: AXIS_H }}>
          <div className="fgraph-axis-gutter" style={{ width: labelW }} />
          <svg width={scale.width} height={AXIS_H} viewBox={`0 0 ${scale.width} ${AXIS_H}`} aria-hidden="true">
            {ticks.map((tk) => (
              <text key={tk.ts} className="fgraph-axis-label" x={tickX(tk, scale)} y={AXIS_H - 6} textAnchor={tk.anchor}>
                {tk.label}
              </text>
            ))}
          </svg>
        </div>
        <div className="fgraph-rows">
          <div className="fgraph-labels" style={{ width: labelW, paddingTop: TOP_PAD }}>
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
                  session={sessionMap.get(lane.id)}
                  onOpen={(e) => activate(e, (np) => openActor(lane.id, np))}
                  onFold={lane.hasChildren ? () => toggleFold(lane.id) : undefined}
                />
              );
            })}
            </div>
            <div
            className="fgraph-canvas"
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={endDrag}
            onPointerCancel={endDrag}
            onClickCapture={onClickCapture}
            >
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
                  ((fromRow == null && !isExternalActor(a.from)) || (toRow == null && !isExternalActor(a.to)));
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

              {/* The guide lines stay with the rows they measure; only their captions moved
                  to the sticky strip above. */}
              {ticks.map((tk) => (
                <line key={tk.ts} className="fgraph-tick" x1={xOf(scale, tk.ts)} x2={xOf(scale, tk.ts)} y1={2} y2={totalH - 2} />
              ))}
            </svg>
          </div>
        </div>
        {model.lanes.length === 0 && <EmptyState icon="graph" title={tr("fgraph.empty")} />}
      </div>
    </div>
  );
}

// Ctrl/⌘/middle click always forces a new pane; a plain click leaves it to the caller
// (which folds in `beside`). Mirrors SessionCard's openIt (features/overview).
function activate(e: ClickMods, run: (newPane: boolean) => void): void {
  e.preventDefault();
  run(e.ctrlKey || e.metaKey || e.button === 1);
}

interface AxisTick {
  ts: number;
  label: string;
  // The two end captions sit ON the edges of the drawable width, where a centred label is
  // half outside the strip and gets clipped — the left one rendered as ":59" of "08:59".
  // They anchor inward instead; everything between stays centred on its line.
  anchor: "start" | "middle" | "end";
}

function axisTicks(from: number, to: number): AxisTick[] {
  const span = to - from;
  const steps = 5;
  const out: AxisTick[] = [];
  for (let i = 0; i <= steps; i++) {
    const ts = from + (span * i) / steps;
    const anchor = i === 0 ? "start" : i === steps ? "end" : "middle";
    out.push({ ts, label: fmtDateTime(ts, span > 3 * DAY_MS ? undefined : TIME_HM), anchor });
  }
  return out;
}

// A tick's caption x: on the line, nudged off the very edge so the first and last glyphs
// are not flush against the strip's border.
const tickX = (tk: AxisTick, scale: GraphScale): number => {
  const x = xOf(scale, tk.ts);
  return tk.anchor === "start" ? x + 2 : tk.anchor === "end" ? x - 2 : x;
};

// The fold control and the hidden-count badge, shared by the erased and the known label so
// an erased row that still holds a family can be folded like any other (decision 6 keeps
// its row for the arrows; nothing there says its children must stay on screen).
function FoldToggle({ lane, onFold }: { lane: GraphLane; onFold: (() => void) | undefined }) {
  const tr = useT();
  if (!onFold) return <span className="fgraph-fold-gap" />;
  const hidden = lane.hiddenDescendants ?? 0;
  return (
    <button
      type="button"
      className="fgraph-fold"
      aria-expanded={!hidden}
      title={hidden ? tr("fgraph.expand") : tr("fgraph.collapse")}
      onClick={(e) => {
        // The row itself opens the session; this button must not do both.
        e.preventDefault();
        e.stopPropagation();
        onFold();
      }}
    >
      <Icon name={hidden ? "chevron-right" : "chevron-down"} />
    </button>
  );
}

function HiddenCount({ lane }: { lane: GraphLane }) {
  const tr = useT();
  const hidden = lane.hiddenDescendants ?? 0;
  if (!hidden) return null;
  return (
    <span className="fgraph-hidden" title={tr("fgraph.hidden_children_hint", { n: hidden })}>
      {tr("fgraph.hidden_children", { n: hidden })}
    </span>
  );
}

function LaneLabel({
  lane,
  h,
  clickable,
  missingParent,
  session,
  onOpen,
  onFold,
}: {
  lane: GraphLane;
  h: number;
  clickable: boolean;
  missingParent: string | null;
  /** The live row behind this lane, when it still has one. Its STATE is not re-derived
   *  here: `stateInfo` (lib/sessionview.ts) is the one place that maps a session to a
   *  chip, and the rail row, the pane head and the overview card are its other three
   *  readers (ADR 0078 decision 5). A fourth derivation would drift from them, and this
   *  figure would say "idle" about a session the rest of the Console calls limited. */
  session: Session | undefined;
  onOpen: (e: ClickMods) => void;
  onFold: (() => void) | undefined;
}) {
  const tr = useT();
  if (lane.erased) {
    return (
      <div className="fgraph-label erased" style={{ height: h }} title={tr("fgraph.erased_label", { id: lane.label })}>
        <FoldToggle lane={lane} onFold={onFold} />
        <Icon name="trash" />
        <span className="fgraph-label-text">{tr("fgraph.erased_label", { id: lane.label })}</span>
        <HiddenCount lane={lane} />
      </div>
    );
  }
  const cls = kindClass(lane.kind);
  // A lane the live list no longer carries (pruned, deleted, or from another workspace)
  // has no session to ask, so the presence word the line style already encodes is what
  // the chip says instead — never a guess at a state nobody reported.
  const st = session ? stateInfo(session) : null;
  const presence = tr(PRESENCE_KEY[lane.presence]);
  return (
    <div
      className={cx("fgraph-label", clickable && "clickable", lane.depth > 0 && "child")}
      style={{ height: h, paddingLeft: 6 + Math.min(lane.depth, 4) * 10 }}
      role={clickable ? "button" : undefined}
      tabIndex={clickable ? 0 : undefined}
      title={`${lane.label}\n${kindLabel(lane.kind)} · ${st ? st.text : presence}`}
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
      <FoldToggle lane={lane} onFold={onFold} />
      {missingParent && (
        <Icon name="warning" className="fgraph-parent-gap" title={tr("fgraph.parent_missing", { id: missingParent })} />
      )}
      <span className={"sess-kic kind-" + cls}>
        <Icon name={kindIcon(lane.kind)} />
      </span>
      <span className="fgraph-label-text">{lane.label}</span>
      <HiddenCount lane={lane} />
      {st ? (
        <span className={"session-state " + st.cls} title={st.text}>
          <Icon name={st.icon} spin={st.spin} />
          {" "}
          <span className="lbl">{st.short || st.text}</span>
        </span>
      ) : (
        <span className="session-state off" title={presence}>
          <span className="lbl">{tr(PRESENCE_SHORT_KEY[lane.presence])}</span>
        </span>
      )}
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
