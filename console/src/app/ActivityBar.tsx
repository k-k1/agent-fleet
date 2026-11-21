// ActivityBar — the icon column at the left edge (ADR 0140). The top group chooses which view
// the left pane shows; Home above it and the group at the foot open a pane and leave the left
// pane alone. Keeping those two apart is the point of the column: an icon either changes the
// left pane or the main area, never both.
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
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

  const views = useMemo(() => (schedulerEnabled ? VIEWS : VIEWS.filter((v) => v.id !== "schedules")), [schedulerEnabled]);

  // Phone only (a tablet's docked column keeps taps), and only while the drawer is out.
  // The list is the one rendered above, so a toggled scheduler is never stale.
  const barRef = useRef<HTMLDivElement>(null);
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
  useBarSwipe(barRef, { enabled: !foldable && shown, step }, [views.length, imagegenAvailable]);

  return (
    <div ref={barRef} className="actbar" role="toolbar" aria-orientation="vertical" aria-label={tr("rail.bar_aria")}>
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
            className={"actbar-btn" + (active ? " active" : "")}
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
  );
});

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
