// ActiveSection — the Active view of the left pane (ADR 0145): the sessions that are running,
// or that still need the member, as cards under the repository or space they work in.
//
// It reads the stores the Sessions view already keeps fresh and asks for nothing itself: the
// view is mounted whether or not anyone opens it, so a request made here would be paid by
// every member. The one piece of state it owns is the parents the member opened (store.ts). The grouping, the scope and the fold are active.ts;
// the card is the sessions overview's, in its rail form, so a state or a gauge never reads
// differently here than in the overview pane.
import { memo, useMemo } from "react";
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
import { useShownSchedules } from "../schedules/store.ts";
import { spacesFor, spacesOwner, useSpacesStore } from "../spaces/store.ts";
import { SessionCard } from "../overview/SessionCard.tsx";
import { activeGroups, isUnattendedKind } from "./active.ts";
import type { ActiveGroup } from "./active.ts";
import { useActiveExpanded, useActiveFoldedGroups, useActiveOpenedGroups } from "./store.ts";
import "../overview/overview.css";

// Deeper generations stop indenting: at the pane's width a fourth step would leave the title no room.
const MAX_INDENT = 3;

const GROUP_ICON: Record<ActiveGroup["kind"], string> = { repo: "repo", space: "folder", other: "terminal-tmux", autorun: "play", schedule: "calendar" };

export const ActiveSection = memo(function ActiveSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  // An empty list that never loaded is "unknown", not "no repositories".
  const reposLoaded = useReposStore((s) => s.loadState === "ok" || s.repos.length > 0);
  // The names the Sessions view's Spaces section keeps asked for (useSpaceNames); until its
  // answer arrives the folders stand in.
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const wset = useActiveWorkingSet();
  const shownSchedules = useShownSchedules();
  // Derived from the layout here rather than through useRepoRailContext, which also reads
  // the connections and the launch catalogue: a card launches nothing.
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
  // Ordinary groups start open and store the folded ones; the unattended two start folded and store the opened ones.
  const isFolded = (g: ActiveGroup) => (isUnattendedKind(g.kind) ? !openedSet.has(g.key) : foldedSet.has(g.key));
  const groupName = (g: ActiveGroup) => (g.kind === "other" ? tr("pj.other_sessions") : g.kind === "autorun" ? tr("memo.autorun") : g.kind === "schedule" ? tr("sched.title") : g.label);
  // Headings are sorted with the locale's collation, so the order is the locale's too.
  const locale = useLocale();

  const expandedSet = useMemo(() => new Set(expanded), [expanded]);
  const groups = useMemo(
    () => activeGroups(sessions, repos, spaces, wset, expandedSet, shownSchedules, reposLoaded),
    // `locale` is not named in the body: compareText reads it.
    [sessions, repos, spaces, wset, expandedSet, shownSchedules, reposLoaded, locale],
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
        <div className="section-empty">{wset ? tr("active.empty_in_set", { set: wset.name }) : tr("active.empty")}</div>
      ) : (
        <div className="act-list">
          {groups.map((g) => (
            <section className="act-group" key={g.key} data-active-group={g.key}>
              <h3 className="ovw-gtitle act-gtitle" title={g.hint || undefined}>
                {/* The heading is a disclosure button: Enter / Space fold it, and a screen reader says which way it is. */}
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
                      waitingAt={waitingAt(s.name)}
                      actions={actions}
                      fold={fold}
                      hiddenWaiting={g.hiddenWaiting.get(s.name)}
                      onFold={fold?.hasChildren ? () => toggle(s.name) : undefined}
                    />
                    </div>
                  );
                })}
              </div>
              )}
            </section>
          ))}
        </div>
      )}
    </Section>
  );
});
