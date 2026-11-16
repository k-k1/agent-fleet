// ActivityBar — the icon column at the left edge (ADR 0140). The top group chooses which view
// the left pane shows; Home above it and the group at the foot open a pane and leave the left
// pane alone. Keeping those two apart is the point of the column: an icon either changes the
// left pane or the main area, never both.
import { memo, useEffect, useMemo } from "react";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import { useActiveWorkingSet } from "../lib/workingSetsStore.ts";
import { useWorkItemStore } from "../features/workitems/store.ts";
import { useSeenStore } from "../features/workitems/seen.ts";
import { dedupeWorkItems, sortWorkItems } from "../features/workitems/read.ts";
import { useSchedulesStore } from "../features/schedules/store.ts";
import { openHome } from "../features/home/open.ts";
import { openSessionsOverview } from "../features/overview/open.ts";
import { openImagegen } from "../features/imagegen/open.ts";
import { useImagegenAvailable } from "../features/imagegen/available.ts";
import { openGeneratedGallery } from "../features/gallery/open.ts";
import { hintSuffix } from "../features/keys/keyHint.ts";
import { Icon } from "../ui/Icon.tsx";
import { tCount, useT } from "../lib/i18n/index.ts";
import type { MsgKey } from "../lib/i18n/index.ts";
import { hasUnreadConversation, hasUnreadSession, pendingNews, useRailAck, waitingSessions, workItemNewsKeys } from "./activity/badges.ts";
import type { RailBadge } from "./activity/badges.ts";

interface ViewDef {
  id: RailView;
  icon: string;
  /** A second, small glyph at the icon's lower right. Sessions are agents at work in a
   * repository and no single codicon says both. */
  mark?: string;
  label: MsgKey;
}

const VIEWS: ViewDef[] = [
  { id: "sessions", icon: "repo", mark: "sparkle", label: "rail.view.sessions" },
  { id: "assistant", icon: "comment-discussion", label: "asst.section_title" },
  { id: "workitems", icon: "issues", label: "wi.title" },
  { id: "memo", icon: "note", label: "memo.title" },
  { id: "schedules", icon: "calendar", label: "sched.title" },
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

  const views = schedulerEnabled ? VIEWS : VIEWS.filter((v) => v.id !== "schedules");
  return (
    <div className="actbar" role="toolbar" aria-orientation="vertical" aria-label={tr("rail.bar_aria")}>
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
            title={badge ? `${name} — ${badgeText(badge, tr)}` : name}
            onClick={() => (foldable ? pickView(v.id) : showView(v.id))}
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

function badgeText(b: RailBadge, tr: ReturnType<typeof useT>): string {
  switch (b.tone) {
    case "waiting":
      return tCount("rail.badge.waiting", b.count);
    case "news":
      return tCount("rail.badge.news", b.count);
    case "failed":
      return tr("rail.badge.failed");
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
  }, [onScreen, workItemKeys, workItemsLoaded, failedRuns, allFailedRuns, ack]);

  const out: Partial<Record<RailView, RailBadge>> = {};
  if (waiting > 0) out.sessions = { tone: "waiting", count: waiting };
  else if (unreadSessions) out.sessions = { tone: "unread" };
  if (unreadConversation) out.assistant = { tone: "unread" };
  const news = onScreen === "workitems" ? 0 : pendingNews(workItemKeys, acked.workitems).length;
  if (news > 0) out.workitems = { tone: "news", count: news };
  if (onScreen !== "schedules" && pendingNews(failedRuns, acked.schedules).length > 0) out.schedules = { tone: "failed" };
  return out;
}
