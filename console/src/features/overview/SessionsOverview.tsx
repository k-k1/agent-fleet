// SessionsOverview — every running session as a card in a grid that reflows with the pane's
// width (ADR 0078). A pane view like the others: it renders a ViewHead, takes the tabbed
// grid's header actions, and reads the session store the rail already keeps fresh (SSE /
// 4-second polling), so watching the fleet here costs no extra request.
//
// The cards are grouped by REPOSITORY (one heading per project, every working copy of it
// included) and, inside a group, laid out family by family — overview.ts, pure and tested.
// The only state the view owns is the stopped-rows toggle, and that lives in the pane's
// CONTENT, not in React state: a tab switch unmounts this component, and a toggle that
// snapped back on every switch would read as broken.
import type { ReactNode } from "react";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useIsMobile } from "../../lib/device.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { paneOrdinals, sessionPanes } from "../../layout/badges.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { useNotificationStore } from "../notifications/store.ts";
import { waitingAtFromNotifications } from "../sessions/order.ts";
import { observedWaitingAt } from "../sessions/waiting.ts";
import { SessionCard } from "./SessionCard.tsx";
import { NO_REPO_GROUP, overviewGroups } from "./overview.ts";
import "./overview.css";

interface SessionsOverviewProps {
  paneId: string;
  showStopped: boolean;
  headerActions?: ReactNode;
}

export function SessionsOverview({ paneId, showStopped, headerActions }: SessionsOverviewProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const wset = useActiveWorkingSet();
  const layout = useLayoutStore((s) => s.layout);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const actions = useSessionActions();
  // Asked once for the whole grid, not per card: the same phone breakpoint layout/ops itself
  // uses to decide that "open in a new pane" means stacking two half-height panes. On a phone
  // that is not what a tap should do, so a plain tap opens in place (ADR 0078 decision 3).
  const beside = !useIsMobile();

  // The headings are built from the working copies (decision 9), so this view loads them
  // itself instead of hoping the rail did: a popped-out tab has no rail at all, and a grid
  // that mounted while the agent was still booting would otherwise keep every session under
  // its raw folder name until the rail's 60-second poll. Transient 502s retry with backoff
  // (the rail's own rule); a stopped workspace settles without clearing the store — emptying
  // it is the rail's business, not ours.
  const repos = useReposStore((st) => st.repos);
  const refreshRepos = useReposStore((st) => st.refresh);
  useRetryLoad(
    async (signal) => {
      const settled = await refreshRepos();
      return signal.aborted || settled || !running;
    },
    [refreshRepos, running],
  );
  // "Waiting since" for the cards. Two ledgers, exactly as the command palette composes them
  // (order.ts): the server's notifications survive a reload and reach another device, this
  // tab's observations fill the gaps notifications leave. Neither is used for ORDERING here —
  // that is what ADR 0078 decision 6 forbids; a card that only shows the number stays put.
  const notifications = useNotificationStore((st) => st.items);
  const fromNotifications = waitingAtFromNotifications(notifications);
  const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));

  const groups = overviewGroups(sessions, repos, wset, showStopped);
  const alive = groups.reduce((n, g) => n + g.alive, 0);
  const empty = groups.length === 0;
  const sPanes = sessionPanes(layout);
  const multi = paneOrdinals(layout).size > 1;

  const toggleStopped = () => setPaneTarget(paneId, { content: { kind: "sessions", showStopped: !showStopped } });

  return (
    <div className="ovw">
      <ViewHead
        actions={
          <>
            <button
              type="button"
              className={"ui-btn ui-btn-ghost ovw-toggle" + (showStopped ? " on" : "")}
              aria-pressed={showStopped}
              title={tr("ovw.show_stopped_hint")}
              onClick={toggleStopped}
            >
              <Icon name={showStopped ? "eye" : "eye-closed"} /> <span className="lbl">{tr("ovw.show_stopped")}</span>
            </button>
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="dashboard" /> {tr("pane.kind.sessions")}
        </span>
        <span className="ovw-count" title={tr("ovw.count_hint")}>
          {tr("ovw.count_alive", { n: alive })}
        </span>
      </ViewHead>
      {empty ? (
        <EmptyState
          icon="dashboard"
          title={wset ? tr("ovw.empty_in_set", { set: wset.name }) : tr("ovw.empty")}
          hint={showStopped ? undefined : tr("ovw.empty_hint")}
        />
      ) : (
        <div className="ovw-body">
          {groups.map((g) => (
            <section className="ovw-group" key={g.key || "~"}>
              {/* One heading per repository. It stays even when there is only one group: the
                  count belongs to the project, and a heading that appears once a second
                  project starts would move every card down. */}
              <h3 className="ovw-gtitle" title={g.hint || undefined}>
                <Icon name="repo" />
                <span className="ovw-gname">{g.key === NO_REPO_GROUP ? tr("pj.other_sessions") : g.label}</span>
                <span className="ovw-gcount">{tr("ovw.group_count", { alive: g.alive, n: g.sessions.length })}</span>
              </h3>
              <div className="ovw-grid" role="list">
                {g.sessions.map((s) => (
                  <SessionCard
                    key={s.name}
                    s={s}
                    opens={sPanes.get(s.name) || []}
                    multi={multi}
                    beside={beside}
                    running={running}
                    waitingAt={waitingAt(s.name)}
                    actions={actions}
                  />
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}
