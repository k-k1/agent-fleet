// Pane — one slot of the main-area layout. The terminal stays mounted (just
// hidden) while the pane shows another view, so the PTY socket and scrollback
// survive switching kinds. Ported from the old console onto the content union.
import { useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, DragEvent as RDragEvent, WheelEvent as RWheelEvent } from "react";
import type { Cell, Pane as PaneT } from "../../layout/types.ts";
import { ordClass } from "../../layout/badges.ts";
import { usePaneHover, hoverMatches } from "../../lib/panehover.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { TerminalView } from "../terminal/TerminalView.tsx";
import { MirrorView } from "../mirror/MirrorView.tsx";
import { agentOf } from "../../agents/registry.ts";
import { SourceControlView } from "../scm/SourceControlView.tsx";
import { ChangesView } from "../scm/ChangesView.tsx";
import { CommitDetailView } from "../scm/CommitDetailView.tsx";
import { WorkingDiffView } from "../scm/WorkingDiffView.tsx";
import { FileView } from "../viewer/FileView.tsx";
import { ReaderView } from "../viewer/ReaderView.tsx";
import { DocView } from "../viewer/DocView.tsx";
import { DiffView } from "../viewer/DiffView.tsx";
import type { DiffEdit } from "../viewer/DiffView.tsx";
import { ChatView } from "../chat/ChatView.tsx";
import { useSettings } from "../../lib/settings.ts";
import { coarsePointer } from "../../lib/device.ts";
import { findScroller, VIEWER_KINDS } from "../../lib/keyScroll.ts";
import { useT } from "../../lib/i18n/index.ts";
import { IconButton } from "../../ui/Button.tsx";
import { cx } from "../../ui/cx.ts";
import { isManagedSession } from "../../types/session.ts";
import type { Session } from "../../types/session.ts";
import { PaneFind } from "./PaneFind.tsx";
import { BrowserPane } from "../browser/BrowserPane.tsx";
import { BrowserAttachPane } from "../browser/BrowserAttachPane.tsx";
import { canPopout, openPanePopout } from "./popout.ts";
import { usePopoutMode } from "../../lib/popoutMode.ts";
import type { PaneView } from "../../layout/types.ts";
import { paneTitle } from "./paneTitle.ts";
import { acceptsPaneDrag, tabOwnsDrop } from "./paneDnd.ts";

// Drag payload MIME — identifies a pane-to-pane drag (vs any other drag).
const DND = "application/x-af-pane";
const TAB_DND = "application/x-af-pane-tab";


interface PaneProps {
  cell: Cell;
  pane?: PaneT;
  style?: CSSProperties;
  active?: boolean;
  single?: boolean;
  /** The tabbed-grid profile shows a tab strip even for its sole view. */
  tabbed?: boolean;
  canSplitRight?: boolean;
  canSplitDown?: boolean;
  canClose?: boolean;
  canDrag?: boolean;
  onActivate: (id: string) => void;
  onClose: (id: string, remove?: boolean) => void;
  onSwap: (aId: string, bId: string) => void;
  onDropSplit: (srcId: string, refId: string, dir: "right" | "down") => void;
  sessionMeta?: Session | null;
  ordinal?: number | null;
}

export function Pane(props: PaneProps) {
  return props.pane ? <PopulatedPane {...props} pane={props.pane} /> : <EmptyPane {...props} />;
}

function EmptyPane({ cell, style, active, tabbed, canSplitRight, canSplitDown, canClose, canDrag, onActivate, onClose, onSwap, onDropSplit, ordinal }: PaneProps) {
  const tr = useT();
  return (
    <div
      className={cx("pane", active && "active", ordinal ? ordClass(ordinal) : "")}
      style={style}
      data-cell-id={cell.id}
      onMouseDownCapture={() => onActivate(cell.id)}
      onDragOver={(e) => { if (e.dataTransfer.types.includes(TAB_DND) || e.dataTransfer.types.includes(DND)) e.preventDefault(); }}
      onDrop={(e) => {
        const tabId = e.dataTransfer.getData(TAB_DND);
        const sourceCellId = e.dataTransfer.getData(DND);
        if (!tabId && !sourceCellId) return;
        e.preventDefault();
        const rect = e.currentTarget.getBoundingClientRect();
        const right = canSplitRight && (e.clientX - rect.left) / rect.width >= 0.7;
        const down = canSplitDown && (e.clientY - rect.top) / rect.height >= 0.7;
        const dir = down ? "down" : right ? "right" : null;
        const store = useLayoutStore.getState();
        if (tabId) dir ? store.dropSplitTab(tabId, cell.id, dir) : store.moveTab(tabId, cell.id);
        else if (sourceCellId) dir ? onDropSplit(sourceCellId, cell.id, dir) : onSwap(sourceCellId, cell.id);
      }}
    >
      {tabbed && <div className="pane-tabs" role="tablist" aria-label={tr("display.pane_layout_tabs")} />}
      {canDrag && <button type="button" className={cx("pane-grip", ordinal ? "pane-ord " + ordClass(ordinal) : "")} draggable onDragStart={(e) => { e.dataTransfer.setData(DND, cell.id); e.dataTransfer.effectAllowed = "move"; }}>{ordinal}</button>}
      <div className="pane-empty">{tr("pane.no_session")}</div>
      {canClose && <div className="pane-controls"><IconButton icon="close" label={tr("ui.close_pane_hint")} onClick={() => onClose(cell.id, true)} /></div>}
    </div>
  );
}

function PopulatedPane({
  cell,
  pane,
  style,
  active,
  single,
  tabbed,
  canSplitRight,
  canSplitDown,
  canClose,
  canDrag,
  onActivate,
  onClose,
  onSwap,
  onDropSplit,
  sessionMeta,
  ordinal,
}: PaneProps) {
  const tr = useT();
  if (!pane) return null;
  const paneRef = useRef<HTMLDivElement>(null);
  const isTerm = pane.content.kind === "terminal";
  // Minimal pop-out tab: hide the pop-out button (the pane already IS its own
  // tab); reappears after 展開 to full-console mode.
  const popoutTabMode = usePopoutMode();
  // Cross-highlight: glow this pane while its rail row / mini-map cell is
  // hovered (and vice-versa). Keyed by pane id or a shared session name.
  const { hover, setHover } = usePaneHover();
  const hovered = hoverMatches(hover, cell.id, pane.session);
  const ordCls = ordinal ? ordClass(ordinal) : "";
  // null when not a drop target; else the pointer's zone: 'center' → swap;
  // 'right'/'down' → tear the dragged pane off into a new split.
  const [zone, setZone] = useState<string | null>(null);

  // Read-only viewer panes (file / diff / scm / …) have no input to autofocus, so opening one
  // left the keyboard with no scroll target — ↑/↓ did nothing until you clicked the content.
  // When such a pane is active or its content changes, move focus onto its scroller so the
  // arrows/PageUp-Down/Space scroll it right away. Content loads async and each view uses a
  // different scroller class, so we locate it by geometry (findScroller) and watch for it to
  // appear via a MutationObserver rather than a single post-render attempt. Never steals focus
  // already inside the pane (a clicked link/button); no-op on touch (would summon the keyboard).
  const contentKey = JSON.stringify(pane.content); // re-focus when the opened target changes
  useEffect(() => {
    if (!active || coarsePointer() || !VIEWER_KINDS.has(pane.content.kind)) return;
    const root = paneRef.current;
    if (!root) return;
    let done = false;
    const tryFocus = () => {
      if (done) return;
      if (root.contains(document.activeElement)) {
        done = true; // focus already inside (e.g. a clicked link) — leave it alone
        return;
      }
      const el = findScroller(root);
      if (!el) return; // scroller not present / not overflowing yet — keep watching
      if (!el.hasAttribute("tabindex")) el.tabIndex = -1; // focusable, but out of Tab order
      el.focus({ preventScroll: true });
      done = true;
    };
    tryFocus(); // synchronous content (e.g. DiffView) lands immediately
    if (done) return;
    const obs = new MutationObserver(tryFocus); // async content (fetch → render) lands here
    obs.observe(root, { childList: true, subtree: true });
    const timer = window.setTimeout(() => obs.disconnect(), 3000);
    return () => {
      obs.disconnect();
      window.clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, contentKey]);

  // Markdown mirror (case-A): a claude session pane can swap its raw terminal for
  // a read-mostly chat view. A stopped session opened as chat starts DETACHED
  // (read-only history, no resume) — 再開して続ける / the ターミナル toggle attaches.
  const chat = pane.content.kind === "terminal" && pane.content.chat;
  const [mirror, setMirror] = useState(chat === true);
  const [attached, setAttached] = useState(!chat || sessionMeta?.alive === true);
  // Re-sync when the pane's session/chat descriptor changes (a new session opened
  // here); local toggles persist within the same session.
  useEffect(() => {
    setMirror(chat === true);
    setAttached(!chat || sessionMeta?.alive === true);
  }, [pane.session, chat]);
  // Track liveness: alive → attach (connecting doesn't resume); stopped → detach
  // to read-only so nothing silently resumes. Runs only on an alive CHANGE, so a
  // user resume (attached=true while alive still false) isn't undone.
  useEffect(() => {
    if (sessionMeta?.alive === true) setAttached(true);
    else if (sessionMeta?.alive === false) setAttached(false);
  }, [sessionMeta?.alive]);
  // Managed（paneless）セッション（docs/27 §10）: tmux pane が存在しないので
  // ターミナルはマウントせず、ミラー（チャット）を常時の主 UI にする。
  const managed = isManagedSession(sessionMeta);
  // The mirror is offered only for agents with the `chat` capability (claude —
  // /messages is claude-only). Guard on loaded sessionMeta.
  const canMirror = isTerm && !!pane.session && !!sessionMeta && agentOf(sessionMeta.kind).caps.chat;
  const showMirror = canMirror && (mirror || managed);
  // The ターミナル toggle also resumes a stopped session (attach is explicit).
  const onToggleMirror = (toChat: boolean) => {
    if (toChat) setMirror(true);
    else {
      setMirror(false);
      onResume();
    }
  };

  // Per-pane line-wrap override for text views (null = follow the global setting).
  const setPaneWrap = useLayoutStore((s) => s.setPaneWrap);
  const selectTab = useLayoutStore((s) => s.selectTab);
  const closeTab = useLayoutStore((s) => s.closePane);
  const moveTab = useLayoutStore((s) => s.moveTab);
  const dropSplitTab = useLayoutStore((s) => s.dropSplitTab);
  const sessions = useSessionsStore((s) => s.sessions);
  const sessionByName = useMemo(() => new Map(sessions.map((s) => [s.name, s] as const)), [sessions]);
  const settings = useSettings();
  const wrapOn = pane.wrap ?? settings.wrap;
  const canWrap =
    pane.content.kind === "file" || pane.content.kind === "diff" || pane.content.kind === "wtdiff";

  // Resume a stopped session EXPLICITLY (the terminal WS is connect-only): POST
  // /start, then attach. An already-alive session just attaches.
  const startSession = useSessionsStore((s) => s.start);
  const onResume = () => {
    void (async () => {
      if (sessionMeta?.alive !== true && pane.session) await startSession(pane.session);
      setAttached(true);
    })();
  };

  // The control cluster overlays the pane's top-right; every view header
  // reserves right padding for it. Button count varies per pane (popout/wrap/
  // close), so publish it as --pane-ctl-n and let CSS derive the reserved
  // width (--pane-ctl-w, panes.css) instead of per-view magic numbers.
  const showPopout = popoutTabMode !== "popout" && canPopout(pane);
  const ctlCount = (showPopout ? 1 : 0) + (canWrap ? 1 : 0) + (canClose ? 1 : 0);
  const views: PaneView[] = cell.views;
  // Never expose the runtime session slug in the tab strip: sessions have a
  // user-facing title, and unloaded metadata should read as a neutral state
  // until it arrives instead of briefly leaking an opaque identifier.
  const tabLabel = (view: PaneView) => {
    const session = view.session ? sessionByName.get(view.session) ?? null : null;
    if (view.content.kind === "terminal" && view.session && !session) return tr("pane.no_session");
    return paneTitle(view, session);
  };
  // The selected session can be shown either as terminal or chat. Keep these
  // cell actions in the respective header, immediately before that switch,
  // so they never disappear merely because the user chose チャット.
  const tabHeaderActions = tabbed ? (
    <span className="tab-pane-actions">
      {showPopout && (
        <IconButton
          icon="link-external"
          label={tr("ui.popout_pane_hint")}
          onClick={() => openPanePopout(pane, "popout")}
        />
      )}
      {canClose && (
        <IconButton
          icon="close"
          label={tr("ui.close_pane_hint")}
          className="pane-close"
          onMouseDown={(e) => e.button === 1 && e.preventDefault()}
          onAuxClick={(e) => {
            if (e.button === 1) { e.preventDefault(); onClose(cell.id, true); }
          }}
          onClick={(e) => onClose(cell.id, e.ctrlKey || e.metaKey)}
        />
      )}
    </span>
  ) : undefined;

  const onDragStart = (e: RDragEvent) => {
    e.dataTransfer.setData(DND, cell.id);
    e.dataTransfer.effectAllowed = "move";
  };
  // A wheel over a tab strip should browse tabs, not scroll the view behind it.
  // Keep the event untouched at either end so a user can still continue into the
  // page/terminal naturally once there are no more tabs in that direction.
  const onTabsWheel = (e: RWheelEvent<HTMLDivElement>) => {
    const strip = e.currentTarget;
    if (strip.scrollWidth <= strip.clientWidth) return;
    const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
    if (!delta) return;
    const before = strip.scrollLeft;
    strip.scrollLeft += delta;
    if (strip.scrollLeft !== before) e.preventDefault();
  };
  // Outer 30% of the splittable edges is a split zone; the center swaps.
  const zoneFor = (e: RDragEvent): "center" | "down" | "right" => {
    const r = paneRef.current?.getBoundingClientRect() ?? e.currentTarget.getBoundingClientRect();
    const rd = canSplitRight ? (e.clientX - r.left) / r.width - 0.7 : -1;
    const dd = canSplitDown ? (e.clientY - r.top) / r.height - 0.7 : -1;
    if (rd < 0 && dd < 0) return "center";
    return dd > rd ? "down" : "right";
  };
  const onDragOver = (e: RDragEvent) => {
    const tabDrag = e.dataTransfer.types.includes(TAB_DND);
    // `canDrag` governs CELL dragging (the ordinal grip is absent for a lone
    // cell), not tab dragging. A tab in the only cell must still be tearable
    // into a right/down split.
    if (!acceptsPaneDrag(!!canDrag, e.dataTransfer.types.includes(DND), tabDrag)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const z = zoneFor(e);
    setZone((prev) => (prev === z ? prev : z));
  };
  const onDragLeave = (e: RDragEvent) => {
    if (e.currentTarget.contains(e.relatedTarget as Node)) return;
    setZone(null);
  };
  const onDrop = (e: RDragEvent) => {
    const tabDrag = e.dataTransfer.types.includes(TAB_DND);
    if (!e.dataTransfer.types.includes(DND) && !tabDrag) return;
    e.preventDefault();
    // Recompute at drop time. dragover state is visual feedback and may lag a
    // frame, especially when the pointer just crossed from a tab into an edge.
    const z = zoneFor(e);
    setZone(null);
    const src = e.dataTransfer.getData(tabDrag ? TAB_DND : DND);
    if (!src) return;
    if (tabDrag) {
      if (z === "right" || z === "down") dropSplitTab(src, cell.id, z);
      else moveTab(src, cell.id);
    } else if (z === "right" || z === "down") onDropSplit(src, cell.id, z);
    else onSwap(src, cell.id);
  };

  return (
    <div
      ref={paneRef}
      className={cx("pane", active && "active", zone && "droptarget", hovered && "pane-hover", ordCls)}
      style={{ ...style, "--pane-ctl-n": ctlCount } as CSSProperties}
      data-pane-id={pane.id}
      data-cell-id={cell.id}
      onMouseDownCapture={() => onActivate(cell.id)}
      onMouseEnter={() => setHover({ session: pane.session || null, paneId: cell.id })}
      onMouseLeave={() => setHover(null)}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {tabbed && (
        <div
          className="pane-tabs"
          role="tablist"
          aria-label={tr("display.pane_layout_tabs")}
          onWheel={onTabsWheel}
        >
          {views.map((view) => (
            <div className={cx("pane-tab", view.id === cell.selectedViewId && "selected")} role="presentation" key={view.id}>
              <button
                type="button"
                role="tab"
                aria-selected={view.id === cell.selectedViewId}
                title={tabLabel(view)}
                draggable
                onDragStart={(e) => { e.dataTransfer.setData(TAB_DND, view.id); e.dataTransfer.effectAllowed = "move"; }}
                onDragOver={(e) => {
                  if (!e.dataTransfer.types.includes(TAB_DND)) return;
                  // The tab owns center drops for reordering. Edge drops belong
                  // to the Cell, even when a wide tab visually covers the edge.
                  if (!tabOwnsDrop(zoneFor(e))) return;
                  e.preventDefault();
                  e.stopPropagation();
                }}
                onDrop={(e) => {
                  const source = e.dataTransfer.getData(TAB_DND);
                  if (!source || !tabOwnsDrop(zoneFor(e))) return;
                  e.preventDefault();
                  e.stopPropagation();
                  moveTab(source, cell.id, view.id);
                }}
                onAuxClick={(e) => {
                  if (e.button === 1) { e.preventDefault(); closeTab(view.id); }
                }}
                onClick={() => selectTab(view.id)}
              >
                {tabLabel(view)}
              </button>
              <button
                type="button"
                className="pane-tab-close"
                aria-label={tr("ui.close_pane_hint")}
                onClick={(e) => { e.stopPropagation(); closeTab(view.id); }}
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      {canDrag && (
        // The drag grip doubles as the pane's ordinal chip: a colored number that
        // matches the rail and mini-map, still draggable to swap.
        <button
          type="button"
          className={cx("pane-grip", ordinal ? "pane-ord " + ordCls : "")}
          title={ordinal ? tr("ui.pane_swap_hint", { ordinal }) : tr("ui.drag_to_swap")}
          draggable
          onDragStart={onDragStart}
        >
          {ordinal ?? <span className="codicon codicon-gripper" aria-hidden="true" />}
        </button>
      )}
      {!(tabbed && isTerm) && <div className="pane-controls">
        {showPopout && (
          <IconButton
            icon="link-external"
            label={tr("ui.popout_pane_hint")}
            onClick={() => openPanePopout(pane, "popout")}
          />
        )}
        {canWrap && (
          <IconButton
            icon="word-wrap"
            label={wrapOn ? tr("ui.unwrap_lines") : tr("ui.wrap_lines")}
            className={wrapOn ? "on" : ""}
            onClick={() => setPaneWrap(pane.id, !wrapOn)}
          />
        )}
        {canClose && (
          <IconButton
            icon="close"
            label={tr("ui.close_pane_hint")}
            className="pane-close"
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                onClose(pane.id, true);
              }
            }}
            onClick={(e) => onClose(pane.id, e.ctrlKey || e.metaKey)}
          />
        )}
      </div>}

      {/* Drop hint while dragging a pane over this one. */}
      {zone && <div className={"drop-indicator zone-" + zone} />}

      {/* Browser find searches the whole page. Keep Ctrl-F scoped to the active
          read-oriented pane instead; raw terminals continue to receive Ctrl-F. */}
      <PaneFind rootRef={paneRef} active={!!(single || active)} enabled={pane.content.kind !== "browser" && (!isTerm || showMirror)} />

      {/* The terminal stays mounted for any terminal-kind pane; other kinds keep
          the pane's xterm warm in the service (not mounted) until their view
          port lands. */}
      {/* The terminal stays MOUNTED (hidden) while the mirror shows, so the PTY
          socket + scrollback survive the toggle. Managed sessions have no PTY at
          all — mounting the terminal would just open a dead WS, so skip it. While
          the meta is still loading we can't know the driver yet: a pane opened AS
          CHAT waits for the meta (今日でもターミナルが一瞬素通しになるだけの区間)
          instead of flashing a terminal that a managed session doesn't have. */}
      {isTerm && !managed && !(chat && !sessionMeta) && (
        <div className="view" hidden={showMirror}>
          <TerminalView
            paneId={pane.id}
            session={pane.session}
            sessionMeta={sessionMeta}
            active={(single || active) && isTerm && !showMirror}
            attached={attached}
            canMirror={canMirror}
            mirror={mirror}
            onToggleMirror={onToggleMirror}
            onResume={onResume}
            headerActions={tabHeaderActions}
          />
        </div>
      )}
      {showMirror && (
        <MirrorView
          paneId={pane.id}
          session={pane.session!}
          sessionMeta={sessionMeta}
          active={single || active}
          mirror={mirror}
          onToggleMirror={onToggleMirror}
          readOnly={!attached}
          onResume={onResume}
          headerActions={tabHeaderActions}
        />
      )}
      {pane.content.kind === "scm" && <SourceControlView repo={pane.content.scmRepo} path={pane.content.scmPath} />}
      {pane.content.kind === "changes" && <ChangesView repo={pane.content.scmRepo} />}
      {pane.content.kind === "commit" && (
        <CommitDetailView repo={pane.content.scmRepo} path={pane.content.scmPath} sha={pane.content.commitSha} wrap={wrapOn} />
      )}
      {pane.content.kind === "wtdiff" && (
        <WorkingDiffView
          repo={pane.content.scmRepo}
          path={pane.content.filePath}
          staged={pane.content.diffStaged}
          wrap={wrapOn}
        />
      )}
      {pane.content.kind === "file" && (
        <FileView
          key={pane.content.filePath}
          paneId={pane.id}
          filePath={pane.content.filePath}
          targetLine={pane.content.targetLine}
          targetColumn={pane.content.targetColumn}
          openMode={pane.content.mode}
          wrap={pane.wrap}
        />
      )}
      {pane.content.kind === "read" && <ReaderView filePath={pane.content.filePath} />}
      {pane.content.kind === "doc" && (
        <DocView
          title={pane.content.docTitle}
          content={pane.content.docContent}
          session={pane.content.docSession}
        />
      )}
      {pane.content.kind === "diff" && (
        <DiffView
          title={pane.content.docTitle}
          tool={pane.content.diffTool}
          edits={pane.content.diffEdits as DiffEdit[]}
          wrap={wrapOn}
        />
      )}
      {pane.content.kind === "chat" && (
        <ChatView
          conversationId={pane.content.conversationId}
          draftAssistantId={pane.content.draftAssistantId}
          paneId={pane.id}
          active={single || active}
        />
      )}
      {pane.content.kind === "browser" && (
        <BrowserPane paneId={pane.id} port={pane.content.port} path={pane.content.path} />
      )}
      {pane.content.kind === "browserAttach" && (
        <BrowserAttachPane paneId={pane.id} attachmentId={pane.content.attachmentId} />
      )}
    </div>
  );
}
