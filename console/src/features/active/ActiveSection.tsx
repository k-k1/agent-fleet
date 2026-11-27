// ActiveSection — the Active view of the left pane (ADR 0145): the sessions that are running,
// or that still need the member, as cards under the repository or space they work in.
//
// It reads the stores the Sessions view already keeps fresh and asks for nothing itself: the
// view is mounted whether or not anyone opens it, so a request made here would be paid by
// every member. The one piece of state it owns is the parents the member opened (store.ts). The grouping, the scope and the fold are active.ts;
// the card is the sessions overview's, in its rail form, so a state or a gauge never reads
// differently here than in the overview pane.
import { memo, useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { tCount, useLocale, useT } from "../../lib/i18n/index.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { paneCount, sessionPanes } from "../../layout/badges.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { useNotificationStore } from "../notifications/store.ts";
import { waitingAtFromNotifications } from "../sessions/order.ts";
import { observedWaitingAt } from "../sessions/waiting.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useQueueSettings } from "../memo/queueSettingsCache.ts";
import { useShownSchedules } from "../schedules/store.ts";
import { spacesFor, spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { SessionCard } from "../overview/SessionCard.tsx";
import { activeGroups, activeLaunchCopies, isUnattendedKind } from "./active.ts";
import type { ActiveGroup } from "./active.ts";
import { ActiveRepoLaunch } from "./ActiveRepoLaunch.tsx";
import { useActiveExpanded, useActiveFoldedGroups, useActiveOpenedGroups } from "./store.ts";
import "../overview/overview.css";

// Deeper generations stop indenting: at the pane's width a fourth step would leave the title no room.
const MAX_INDENT = 3;

const GROUP_ICON: Record<ActiveGroup["kind"], string> = { repo: "repo", space: "folder", other: "terminal-tmux", autorun: "play", schedule: "calendar" };

/** The clock the 24-hour window is read against. The list can stay byte-identical for hours (a
 *  stopped workspace, or a push that is skipped when nothing changed), so the window has to move
 *  on its own: a minute tick, and a read when the tab comes back from hidden (timers are throttled
 *  there). */
function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const tick = () => setNow(Date.now());
    const t = setInterval(tick, everyMs);
    const onVisible = () => {
      if (!document.hidden) tick();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [everyMs]);
  return now;
}

export const ActiveSection = memo(function ActiveSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  // An empty list that never loaded is "unknown", not "no repositories".
  const reposLoaded = useReposStore((s) => s.loaded);
  // The names the Sessions view's Spaces section keeps asked for (useSpaceNames); until its
  // answer arrives the folders stand in.
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const wset = useActiveWorkingSet();
  const shownSchedules = useShownSchedules();
  const tenant = useTenantStore((s) => s.tenant);
  const identity = useTenantStore((s) => s.identityRev);
  const { queues } = useQueueSettings((tenant ?? "") + "\u0000" + identity);
  // The app-level StartHost owns connection and catalogue reads for the launch dialog.
  const running = useWorkspaceStore((s) => s.state) === "running";
  const layout = useLayoutStore((s) => s.layout);
  const sPanes = useMemo(() => (paneCount(layout) > 1 ? sessionPanes(layout) : null), [layout]);
  const activeSession = activePane(layout)?.session ?? null;
  const actions = useSessionActions();
  const [expanded, toggle] = useActiveExpanded();
  const [foldedGroups, toggleGroup] = useActiveFoldedGroups();
  const [openedGroups, toggleOpened] = useActiveOpenedGroups();
  const foldedSet = useMemo(() => new Set(foldedGroups), [foldedGroups]);
  const openedSet = useMemo(() => new Set(openedGroups), [openedGroups]);
  // Ordinary groups start open and store the folded ones; unattended groups start folded and store the opened ones.
  const isFolded = (g: ActiveGroup) => (isUnattendedKind(g.kind) ? !openedSet.has(g.key) : foldedSet.has(g.key));
  const groupName = (g: ActiveGroup) => {
    if (g.kind === "other") return tr("pj.other_sessions");
    if (g.kind === "schedule") return tr("sched.title");
    if (g.kind !== "autorun") return g.label;
    if (g.hint) return tr("active.queue_group", { category: g.label, repo: g.hint });
    return g.label || tr("memo.autorun");
  };
  // Headings and checkout choices are sorted with the locale's collation.
  const locale = useLocale();
  const launchCopies = useMemo(() => activeLaunchCopies(repos), [repos, locale]);

  const now = useNow(60_000);
  const expandedSet = useMemo(() => new Set(expanded), [expanded]);
  const groups = useMemo(
    () => activeGroups(sessions, repos, spaces, wset, expandedSet, shownSchedules, reposLoaded, now, queues),
    // `locale` is not named in the body: compareText reads it.
    [sessions, repos, spaces, wset, expandedSet, shownSchedules, reposLoaded, now, queues, locale],
  );
  // The unattended groups are left out of the section's count: it says how many sessions of
  // the member's own are running, and a queue's runs would bury it.
  const alive = groups.reduce((n, g) => n + (isUnattendedKind(g.kind) ? 0 : g.alive), 0);

  // "Waiting since", composed as the overview composes it. Shown, never ordered by.
  const notifications = useNotificationStore((st) => st.items);
  const fromNotifications = useMemo(() => waitingAtFromNotifications(notifications), [notifications]);
  const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));

  return (
    <Section id="active" title={tr("rail.view.active")} icon="pulse" count={alive}>
      {groups.length === 0 ? (
        <div className="section-empty">{!running ? tr("active.empty_stopped") : wset ? tr("active.empty_in_set", { set: wset.name }) : tr("active.empty")}</div>
      ) : (
        <div className="act-list">
          {groups.map((g) => (
            <section className="act-group" key={g.key} data-active-group={g.key}>
              {/* The full queue heading is available when its text is truncated. */}
              <div className="ovw-gtitle act-gtitle">
                <h3 className="act-gheading" title={(g.kind === "autorun" ? groupName(g) : g.hint) || undefined}>
                  <button type="button" className="act-gfold" aria-expanded={!isFolded(g)}
                    aria-label={[groupName(g), tr("ovw.group_count", { alive: g.alive, n: g.total }), g.waiting > 0 ? tCount("active.group_waiting_hint", g.waiting) : ""].filter(Boolean).join(", ")}
                    onClick={() => (isUnattendedKind(g.kind) ? toggleOpened(g.key) : toggleGroup(g.key))}>
                    <Icon name={isFolded(g) ? "chevron-right" : "chevron-down"} />
                    <Icon name={GROUP_ICON[g.kind]} />
                    <span className="ovw-gname">{groupName(g)}</span>
                    <span className="ovw-gcount">{tr("ovw.group_count", { alive: g.alive, n: g.total })}</span>
                    {g.waiting > 0 && (
                      <span className="act-gwait" title={tCount("active.group_waiting_hint", g.waiting)}>
                        <Icon name="question" />
                        {g.waiting}
                      </span>
                    )}
                  </button>
                </h3>
                {g.kind === "repo" && <ActiveRepoLaunch label={g.hint} copies={launchCopies.get(g.key) ?? []} running={running} />}
              </div>
              {!isFolded(g) && (
              <div className="act-cards" role="list">
                {g.sessions.map((s) => {
                  const fold = g.fold.get(s.name);
                  return (
                    // The wrapper carries the indent: the card's own inline style is the lineage colour.
                    <div key={s.name} className="act-item" role="listitem" style={{ "--act-depth": Math.min(g.depth.get(s.name) || 0, MAX_INDENT) } as CSSProperties}>
                    <SessionCard
                      s={s}
                      rail
                      selected={activeSession === s.name}
                      opens={sPanes?.get(s.name) || []}
                      beside={false}
                      running={running}
                      readOnly={!running}
                      waitingAt={waitingAt(s.name)}
                      actions={actions}
                      fold={fold}
                      hiddenWaiting={g.hiddenWaiting.get(s.name)}
                      onFold={fold?.hasChildren ? () => toggle(s.name) : undefined}
                    />
                    </div>
                  );
                })}
                {g.older > 0 && (
                  <div className="act-older" role="listitem">
                    {tCount("active.older_stopped", g.older)}
                  </div>
                )}
              </div>
              )}
            </section>
          ))}
        </div>
      )}
    </Section>
  );
});
