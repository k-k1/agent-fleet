// ActivityBar — the icon column at the left edge (ADR 0140). The top group chooses which view
// the left pane shows; Home above it and the group at the foot open a pane and leave the left
// pane alone. Keeping those two apart is the point of the column: an icon either changes the
// left pane or the main area, never both.
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent, PointerEvent as RPointerEvent, RefObject, TouchEvent as RTouchEvent } from "react";
import { setSettings, useSettings, useUIPrefsUnsettled } from "../lib/settings.ts";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import { useActiveWorkingSet } from "../lib/workingSetsStore.ts";
import { useWorkItemStore } from "../features/workitems/store.ts";
import { useSeenStore } from "../features/workitems/seen.ts";
import { dedupeWorkItems, sortWorkItems } from "../features/workitems/read.ts";
import { pendingHandoffCount, useHandoffStore } from "../features/sharing/handoffStore.ts";
import { useSchedulesStore } from "../features/schedules/store.ts";
import { pendingProposals, useMemoryStore, useProposalsFeed } from "../features/memory/store.ts";
import { memoScope, useMemoStore, useMemoWaitingFeed } from "../features/memo/store.ts";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { openHome } from "../features/home/open.ts";
import { openSessionsOverview } from "../features/overview/open.ts";
import { openImagegen } from "../features/imagegen/open.ts";
import { useImagegenAvailable } from "../features/imagegen/available.ts";
import { useReposStore } from "../features/repos/store.ts";
import { openGeneratedGallery } from "../features/gallery/open.ts";
import { hintSuffix } from "../features/keys/keyHint.ts";
import { Icon } from "../ui/Icon.tsx";
import { tCount, useT } from "../lib/i18n/index.ts";
import type { MsgKey } from "../lib/i18n/index.ts";
import { hasUnreadConversation, hasUnreadSession, pendingNews, useRailAck, waitingSessions, workItemNewsKeys } from "./activity/badges.ts";
import type { RailBadge } from "./activity/badges.ts";
import { useAutoRunFeed, useScopedAutoRuns } from "../features/autorun/store.ts";
import { waitingScheduledCount } from "../features/schedules/scheduledSessions.ts";
import { buildRuns, failedRunKeys, scopeRuns, waitingRunCount } from "../features/autorun/runs.ts";
import { stepView } from "./barSwipe.ts";
import { createLongPress } from "../lib/longPress.ts";
import { isContextMenuKey, menuAnchor } from "../features/project/contextMenuKey.ts";
import { ActivityBarMenu } from "./ActivityBarMenu.tsx";
import type { BarMenuAnchor, BarMenuRow } from "./ActivityBarMenu.tsx";
import { canResetOff, hiddenWaiting, menuEntries, resetOff, shownViews, toggleOff } from "./activity/visibility.ts";
import { useBarSwipe } from "./useBarSwipe.ts";

interface ViewDef {
  id: RailView;
  icon: string;
  /** A second, small glyph at the icon's lower right. Marks the views that list agents at
   * work (sessions, shared sessions, auto-run runs, scheduled sessions); no single codicon
   * says both "agent" and the view's own subject. Views that list other things carry none. */
  mark?: string;
  label: MsgKey;
}

const VIEWS: ViewDef[] = [
  { id: "sessions", icon: "repo", mark: "sparkle", label: "rail.view.sessions" },
  { id: "shared", icon: "broadcast", mark: "sparkle", label: "share.shared_sessions" },
  { id: "assistant", icon: "comment-discussion", label: "asst.section_title" },
  { id: "workitems", icon: "issues", label: "wi.title" },
  { id: "memo", icon: "note", label: "memo.title" },
  { id: "autorun", icon: "play", mark: "sparkle", label: "memo.autorun" },
  { id: "memory", icon: "archive", label: "rail.view.memory" },
  { id: "schedules", icon: "calendar", mark: "sparkle", label: "sched.title" },
  { id: "files", icon: "files", label: "pj.files" },
];

interface ActivityBarProps {
  /** The view the left pane shows. From the shell rather than the store: the shell is what
   * knows a stored choice cannot be shown (the schedules view where there is no scheduler). */
  view: RailView;
  /** Is the left pane's view on screen right now (docked open, or the phone drawer out)? News
   * is acknowledged only for a view somebody can actually see. */
  shown: boolean;
  /** A phone has no folded state for the pane inside its drawer, so there an icon only ever
   * selects; on a desktop the icon of the view on screen folds the pane. */
  foldable: boolean;
  schedulerEnabled: boolean;
}

export const ActivityBar = memo(function ActivityBar({ view, shown, foldable, schedulerEnabled }: ActivityBarProps) {
  const tr = useT();
  const pickView = useLeftRail((s) => s.pickView);
  const showView = useLeftRail((s) => s.showView);
  const imagegenAvailable = useImagegenAvailable();
  const badges = useRailBadges(shown ? view : null);

  // Phone only (a tablet's docked column keeps taps), and only while the drawer is out.
  const barRef = useRef<HTMLDivElement>(null);
  // The view on screen keeps its icon and cannot be turned off; with the left pane folded away
  // nothing is on screen, so any icon may go (the last one excepted).
  const onScreen = shown ? view : null;
  const usable = useMemo(() => (schedulerEnabled ? VIEWS : VIEWS.filter((v) => v.id !== "schedules")), [schedulerEnabled]);
  // The icons the member left on (its context menu). The swipe steps through these only.
  const { railViewsOff: off } = useSettings();
  const views = useMemo(() => {
    const ids = shownViews(
      usable.map((v) => v.id),
      off,
      onScreen,
    );
    return usable.filter((v) => ids.includes(v.id));
  }, [usable, off, onScreen]);
  // The list has not been read from the server yet, or is being read again (a tenant switch): a
  // change now would be built on the previous copy, recorded against the new owner, and win over
  // what the read brings, so the next save would drop ids another browser stored.
  const prefsLoading = useUIPrefsUnsettled();
  const menu = useBarMenu(barRef);
  const entries = useMemo(
    () =>
      menuEntries(
        VIEWS.map((v) => v.id),
        usable.map((v) => v.id),
        off,
        onScreen,
      ),
    [usable, off, onScreen],
  );
  // Views with no icon that wait on the member: the bar carries one dot for them (ADR 0142).
  const waitingHidden = useMemo(
    () =>
      hiddenWaiting(
        usable.map((v) => v.id),
        views.map((v) => v.id),
        badges,
      ),
    [usable, views, badges],
  );
  const waitingNames = waitingHidden.map((w) => tr(usable.find((v) => v.id === w.id)!.label)).join(", ");
  const rows: BarMenuRow[] = entries.map((e) => {
    const def = VIEWS.find((v) => v.id === e.id)!;
    const w = waitingHidden.find((x) => x.id === e.id);
    return { ...e, icon: def.icon, name: tr(def.label), waiting: w ? badgeText(w.badge, tr, e.id) : "" };
  });

  // The list is the one rendered above, so a toggled scheduler is never stale.
  const [announced, setAnnounced] = useState("");
  const step = useCallback(
    (delta: 1 | -1) => {
      const next = stepView(
        views.map((v) => v.id),
        view,
        delta,
      );
      if (next === null) return false;
      showView(next);
      const def = views.find((v) => v.id === next);
      if (def) setAnnounced(tr(def.label));
      return true;
    },
    [views, view, showView, tr],
  );
  // The announcement is for the moment of the swipe only. Cleared afterwards, a repeat swipe
  // to the same view changes the text again (and is read again), and no stale name lingers
  // at the end of the toolbar.
  useEffect(() => {
    if (!announced) return;
    const id = window.setTimeout(() => setAnnounced(""), 1000);
    return () => window.clearTimeout(id);
  }, [announced]);
  // The dot adds height without changing the icon count: it has to re-measure whether the column
  // still fits, or the bar keeps claiming touch-action that the swipe then refuses.
  useBarSwipe(barRef, { enabled: !foldable && shown, step }, [views.length, imagegenAvailable, waitingHidden.length > 0]);

  // A fold or a toggle elsewhere can take the focused icon out of the bar (the view on screen
  // keeps an icon only while it is on screen): keep focus on the bar rather than letting it drop
  // to <body>.
  const lastFocus = useRef<HTMLElement | null>(null);
  useEffect(() => {
    const was = lastFocus.current;
    if (!was || was.isConnected || document.activeElement !== document.body) return;
    lastFocus.current = null;
    const bar = barRef.current;
    (bar?.querySelector<HTMLElement>(".actbar-btn[data-rail-view]") ?? bar?.querySelector<HTMLElement>(".actbar-btn"))?.focus();
  }, [views]);

  // Up / Down move focus over the view icons that are shown (not the launchers), without
  // choosing the view: Enter or Space does that.
  const moveFocus = (e: RKeyboardEvent<HTMLDivElement>) => {
    if ((e.key !== "ArrowDown" && e.key !== "ArrowUp") || !(e.target instanceof HTMLElement) || !e.target.dataset.railView) return;
    const icons = [...e.currentTarget.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")];
    const next = icons[icons.indexOf(e.target) + (e.key === "ArrowDown" ? 1 : -1)];
    e.preventDefault();
    next?.focus();
  };

  return (
    <>
    <div
      ref={barRef}
      className="actbar"
      role="toolbar"
      aria-orientation="vertical"
      aria-label={tr("rail.bar_aria")}
      {...menu.barProps}
      onFocus={(e) => {
        if (e.target instanceof HTMLElement) lastFocus.current = e.target;
      }}
      // Focus that left on its own (a click on blank space, another control) is not ours to
      // restore; focus lost because the button was removed is.
      onBlur={(e) => {
        if (e.target instanceof HTMLElement && (e.relatedTarget !== null || e.target.isConnected)) lastFocus.current = null;
      }}
      onKeyDown={(e) => {
        menu.barProps.onKeyDown(e);
        moveFocus(e);
      }}
    >
      <LaunchButton id="home" icon="home" label={tr("wsbar.home_title") + hintSuffix("open.home")} name={tr("wsbar.home")} onClick={() => openHome()} />
      <span className="actbar-rule" />
      {views.map((v) => {
        const badge = badges[v.id];
        const active = shown && view === v.id;
        const name = tr(v.label);
        return (
          <button
            key={v.id}
            type="button"
            className={"actbar-btn" + (active ? " actbar-btn--active" : "")}
            data-rail-view={v.id}
            aria-pressed={active}
            aria-label={name}
            title={badge ? `${name} — ${badgeText(badge, tr, v.id)}` : name}
            onClick={() => {
              setAnnounced("");
              if (foldable) pickView(v.id);
              else showView(v.id);
            }}
          >
            <Icon name={v.icon} />
            {v.mark && <Icon name={v.mark} className="actbar-mark" />}
            {badge && (
              <span className={"actbar-badge tone-" + badge.tone} data-badge={badge.tone}>
                {badge.count != null ? (badge.count > 99 ? "99+" : badge.count) : ""}
              </span>
            )}
          </button>
        );
      })}
      {waitingHidden.length > 0 && (
        <button
          type="button"
          className="actbar-btn actbar-hidden"
          data-hidden-waiting={waitingHidden.map((w) => w.id).join(" ")}
          aria-label={tr("rail.hidden_waiting", { names: waitingNames })}
          title={tr("rail.hidden_waiting", { names: waitingNames })}
          onClick={(e) => menu.openAt(menuAnchor(e.currentTarget))}
        >
          <Icon name="ellipsis" />
          <span className="actbar-badge tone-waiting" data-badge="waiting" />
        </button>
      )}
      <span className="actbar-gap" />
      <span className="actbar-rule" />
      <LaunchButton
        id="overview"
        icon="layout"
        label={tr("wsbar.overview_title") + hintSuffix("open.sessions")}
        name={tr("wsbar.overview")}
        onClick={() => openSessionsOverview()}
      />
      {/* No engine, no button (ADR 0081 decision 1): a button that opens onto "the engine
          cannot be used" is a promise the bar cannot keep. */}
      {imagegenAvailable && (
        <LaunchButton
          id="imagegen"
          icon="wand"
          label={tr("wsbar.imagegen_title") + hintSuffix("open.imagegen")}
          name={tr("wsbar.imagegen")}
          onClick={() => void openImagegen()}
        />
      )}
      {/* Not behind imagegenAvailable: the folder also holds what the sessions generated,
          which exists whether or not this deployment runs an image engine of its own. */}
      <LaunchButton
        id="generated"
        icon="file-media"
        label={tr("pane.open_generated")}
        name={tr("pane.open_generated")}
        onClick={() => openGeneratedGallery()}
      />
      <span className="actbar-live" role="status" aria-live="polite">
        {announced}
      </span>
    </div>
    {/* A sibling of the bar, not a child: the menu's clicks and keys are React events that would
        otherwise bubble through the portal into the bar's own gesture handlers. */}
    {menu.anchor && (
      <ActivityBarMenu
        anchor={menu.anchor}
        rows={rows}
        busy={prefsLoading}
        canReset={canResetOff(off)}
        onToggle={(id, on) => setSettings({ railViewsOff: toggleOff(off, id, on) })}
        onReset={() => setSettings({ railViewsOff: resetOff(off) })}
        onClose={menu.close}
      />
    )}
    </>
  );
});

// Opens the bar's menu: right-click, the context-menu key (the focused icon anchors it), and a long
// press on a touch screen. The click a long press leaves on lift is dropped, or it would also pick
// the icon under the finger; the drop only covers that click (a short window after the lift, and
// only for events inside the bar), never a later mouse or key action.
const CLICK_SWALLOW_MS = 400;

function useBarMenu(barRef: RefObject<HTMLElement | null>) {
  const [anchor, setAnchor] = useState<BarMenuAnchor | null>(null);
  const [press] = useState(createLongPress);
  const swallow = useRef(false);
  const swallowTimer = useRef<number | null>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  // The button a long press began on: the only one whose click is dropped. The press may start on
  // the icon inside it and the compatibility click may land on the button, so both are the button.
  const pressed = useRef<Element | null>(null);
  const buttonOf = (t: EventTarget | null): Element | null => (t instanceof Element ? t.closest(".actbar-btn") : null);
  const setSwallow = (on: boolean) => {
    swallow.current = on;
    if (swallowTimer.current !== null) window.clearTimeout(swallowTimer.current);
    swallowTimer.current = null;
  };
  useEffect(() => () => setSwallow(false), []);
  const openAt = useCallback(
    (a: BarMenuAnchor) => {
      press.cancel();
      const active = document.activeElement;
      returnFocus.current = active instanceof HTMLElement && barRef.current?.contains(active) ? active : null;
      setAnchor(a);
    },
    [press, barRef],
  );
  const close = useCallback(() => {
    press.cancel();
    setAnchor(null);
    // The opener may be gone by now (its icon was turned off, or the dot it opened from was
    // cleared): land on the view on screen, or the first button, rather than on <body>.
    const back = returnFocus.current;
    returnFocus.current = null;
    if (!back) return;
    const bar = barRef.current;
    const target = back.isConnected ? back : (bar?.querySelector<HTMLElement>(".actbar-btn[aria-pressed='true']") ?? bar?.querySelector<HTMLElement>(".actbar-btn"));
    target?.focus();
  }, [press, barRef]);
  const inBar = (e: { currentTarget: Element; target: EventTarget }) => e.target instanceof Node && e.currentTarget.contains(e.target);
  const barProps = {
    onContextMenu: (e: RMouseEvent) => {
      e.preventDefault();
      if ((e.nativeEvent as PointerEvent).pointerType === "touch") {
        setSwallow(true);
        pressed.current = buttonOf(e.target);
      }
      // A keyboard-raised contextmenu carries no pointer position; anchor on the focused icon.
      const focused = document.activeElement;
      if (e.clientX === 0 && e.clientY === 0 && focused instanceof HTMLElement && e.currentTarget.contains(focused)) openAt(menuAnchor(focused));
      else openAt({ x: e.clientX, y: e.clientY });
    },
    onKeyDown: (e: RKeyboardEvent) => {
      if (!isContextMenuKey(e) || !(e.target instanceof HTMLElement)) return;
      e.preventDefault();
      openAt(menuAnchor(e.target));
    },
    onMouseDown: () => setSwallow(false),
    onPointerDown: (e: RPointerEvent) => press.pointerDown(e.pointerType),
    onTouchStart: (e: RTouchEvent) => {
      setSwallow(false);
      press.cancel();
      pressed.current = buttonOf(e.target);
      const t = e.touches[0];
      if (!t || e.touches.length > 1) return;
      const { clientX, clientY } = t;
      press.start(clientX, clientY, () => {
        setSwallow(true);
        openAt({ x: clientX, y: clientY });
      });
    },
    // The vertical swipe that steps views starts as a drag: past the tolerance it is no press.
    onTouchMove: (e: RTouchEvent) => {
      const t = e.touches[0];
      if (t) press.move(t.clientX, t.clientY);
    },
    onTouchEnd: (e: RTouchEvent) => {
      if (swallow.current) {
        if (e.cancelable) e.preventDefault();
        // The lift's click, where the browser still sends one, comes right after; whatever
        // arrives later is a different action.
        swallowTimer.current = window.setTimeout(() => setSwallow(false), CLICK_SWALLOW_MS);
      }
      press.end();
    },
    onTouchCancel: () => {
      setSwallow(false);
      press.end();
    },
    onClickCapture: (e: RMouseEvent) => {
      if (!swallow.current || !inBar(e)) return;
      // Only the click a finger lifts into is dropped: a keyboard or assistive-technology
      // activation (trusted, detail 0) or a mouse click is a real action.
      const pt = (e.nativeEvent as PointerEvent).pointerType;
      if ((pt !== undefined && pt !== "" && pt !== "touch") || (e.nativeEvent.isTrusted && e.detail === 0)) {
        setSwallow(false);
        return;
      }
      const hit = buttonOf(e.target);
      if (!hit || hit !== pressed.current) return;
      setSwallow(false);
      e.preventDefault();
      e.stopPropagation();
    },
  };
  return { anchor, openAt, close, barProps };
}

function LaunchButton({ id, icon, label, name, onClick }: { id: string; icon: string; label: string; name: string; onClick: () => void }) {
  return (
    <button
      type="button"
      className="actbar-btn launch"
      data-launch={id}
      title={label.startsWith(name) ? label : `${name} — ${label}`}
      aria-label={name}
      onClick={onClick}
    >
      <Icon name={icon} />
    </button>
  );
}

function badgeText(b: RailBadge, tr: ReturnType<typeof useT>, view: RailView): string {
  switch (b.tone) {
    case "waiting":
      // What waits differs by view: sessions on an answer, proposals and memos on a decision.
      return tCount(view === "shared" ? "rail.badge.handoffs" : view === "memory" ? "rail.badge.proposals" : view === "memo" ? "rail.badge.memos" : view === "autorun" ? "rail.badge.runs" : "rail.badge.waiting", b.count);
    case "news":
      return tCount("rail.badge.news", b.count);
    case "failed":
      return tr(view === "autorun" ? "rail.badge.run_failed" : "rail.badge.failed");
    default:
      return tr("rail.badge.unread");
  }
}

// The badge of each view. `onScreen` is the view somebody is looking at (null while the pane
// is folded away): its news is acknowledged as it arrives, so it never shows a news badge.
function useRailBadges(onScreen: RailView | null): Partial<Record<RailView, RailBadge>> {
  const wset = useActiveWorkingSet();
  const sessions = useSessionsStore((s) => s.sessions);
  const waiting = useMemo(() => waitingSessions(sessions, wset), [sessions, wset]);
  // Selectors that answer a boolean: the notification list is replaced on every poll, and a
  // subscription to the list itself would re-render the column each time.
  const unreadSessions = useNotificationStore((s) => hasUnreadSession(s.items, sessions, wset));
  const unreadConversation = useNotificationStore((s) => hasUnreadConversation(s.items, wset));
  const payload = useWorkItemStore((s) => s.payload);
  const seen = useSeenStore((s) => s.map);
  const failedRuns = useSchedulesStore((s) => s.failed);
  const allFailedRuns = useSchedulesStore((s) => s.failedAll);
  // A count, not the list: the list is replaced on every read.
  // Read here, not by the Memory view: the view mounts on first show, the badge cannot wait for it.
  useProposalsFeed(
    useWorkspaceStore((s) => s.state) === "running",
    useTenantStore((s) => s.tenant),
  );
  // The memo list is read by the panel while it is mounted; the bar needs the count before that.
  const running = useWorkspaceStore((s) => s.state) === "running";
  const tenant = useTenantStore((s) => s.tenant);
  useMemoWaitingFeed(running, tenant);
  // A count read for another scope (stopped, another tenant) is not this one's, even for the
  // render before the feed has caught up.
  const memoScopeNow = memoScope(running, tenant);
  const waitingMemos = useMemoStore((s) => (s.waiting && s.waiting.scope === memoScopeNow ? s.waiting.n : 0));
  const pendingMemory = useMemoryStore((s) => pendingProposals(s.proposals));
  // Handoffs offered to the member wait on their decision. Read by the shared view's section,
  // which App mounts for the whole session. There is no news dot for shared sessions: nothing
  // client-side marks one unread (notifications never target the shared list).
  const pendingHandoffs = useHandoffStore((s) => pendingHandoffCount(s.received));
  // The runs the Auto-run view lists, read here too: the badge cannot wait for the view to mount.
  useAutoRunFeed(running);
  const { queues: runQueues, deliveries: queueRuns, loaded: autoRunsLoaded } = useScopedAutoRuns();
  // The rows the view lists under the active working set, so the count and the acknowledgement match them.
  const runs = useMemo(() => scopeRuns(buildRuns(sessions, queueRuns), runQueues, wset), [sessions, queueRuns, runQueues, wset]);
  const waitingRuns = useMemo(() => waitingRunCount(runs), [runs]);
  const failedAutoRuns = useMemo(() => failedRunKeys(runs), [runs]);
  const repos = useReposStore((s) => s.repos);
  const waitingScheduled = useMemo(() => waitingScheduledCount(sessions, wset, repos), [sessions, wset, repos]);
  const acked = useRailAck((s) => s.acked);
  const ack = useRailAck((s) => s.ack);

  // The same rows the view lists: one per ticket, however many queries returned it.
  const workItemKeys = useMemo(
    () => workItemNewsKeys(dedupeWorkItems(sortWorkItems(payload?.items || [])), seen),
    [payload, seen],
  );
  // Only against a list that has loaded. Before that the keys are empty because nothing is
  // known, and acknowledging "nothing" would prune what was acknowledged before the reload.
  const workItemsLoaded = payload !== null;
  useEffect(() => {
    if (onScreen === "workitems" && workItemsLoaded) ack("workitems", workItemKeys);
    if (onScreen === "schedules" && allFailedRuns !== null) ack("schedules", failedRuns, allFailedRuns);
    // Nothing is pruned: a read that lacks a run (the newest fifty, a history still on its way)
    // says nothing about whether the run is gone, and a forgotten key would raise its dot again.
    if (onScreen === "autorun" && autoRunsLoaded && failedAutoRuns.length > 0) ack("autorun", failedAutoRuns, [...(acked.autorun ?? []), ...failedAutoRuns]);
  }, [onScreen, workItemKeys, workItemsLoaded, failedRuns, allFailedRuns, failedAutoRuns, autoRunsLoaded, acked.autorun, ack]);

  const out: Partial<Record<RailView, RailBadge>> = {};
  if (waiting > 0) out.sessions = { tone: "waiting", count: waiting };
  else if (unreadSessions) out.sessions = { tone: "unread" };
  if (unreadConversation) out.assistant = { tone: "unread" };
  const news = onScreen === "workitems" ? 0 : pendingNews(workItemKeys, acked.workitems).length;
  if (news > 0) out.workitems = { tone: "news", count: news };
  // Like a waiting session (ADR 0141 decision 2): a proposal waits on the member's decision, so
  // the count stays while the view is open and clears only as proposals are decided.
  // Notes in an auto-run category that wait for approval (ADR 0140, note of 2026-11-20): nothing
  // runs until the member approves, so the count stays while the view is open. Not set-scoped: the
  // memo view lists every note whatever the working set.
  if (waitingMemos > 0) out.memo = { tone: "waiting", count: waitingMemos };
  if (pendingHandoffs > 0) out.shared = { tone: "waiting", count: pendingHandoffs };
  // A run's session waits on the member's answer: stays while the view is open, like a waiting
  // session. A failed run is news: the dot clears when the view has been looked at.
  if (waitingRuns > 0) out.autorun = { tone: "waiting", count: waitingRuns };
  else if (onScreen !== "autorun" && autoRunsLoaded && pendingNews(failedAutoRuns, acked.autorun).length > 0) out.autorun = { tone: "failed" };
  if (pendingMemory > 0) out.memory = { tone: "waiting", count: pendingMemory };
  // A scheduled run's session waits on the member's answer; the Sessions view does not list it.
  if (waitingScheduled > 0) out.schedules = { tone: "waiting", count: waitingScheduled };
  else if (onScreen !== "schedules" && pendingNews(failedRuns, acked.schedules).length > 0) out.schedules = { tone: "failed" };
  return out;
}
