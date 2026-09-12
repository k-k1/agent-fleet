// SessionsOverview — every running session as a card in a grid that reflows with the pane's
// width (ADR 0078). A pane view like the others: it renders a ViewHead, takes the tabbed
// grid's header actions, and reads the session store the rail already keeps fresh (SSE /
// 4-second polling), so watching the fleet here costs no extra request.
//
// Which sessions and in what order is overview.ts (pure, tested). The only state the view owns
// is the stopped-rows toggle, and that lives in the pane's CONTENT, not in React state: a tab
// switch unmounts this component, and a toggle that snapped back on every switch would read
// as broken.
import type { ReactNode } from "react";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useIsMobile } from "../../lib/device.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { paneOrdinals, sessionPanes } from "../../layout/badges.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionCard } from "./SessionCard.tsx";
import { aliveCount, overviewSessions } from "./overview.ts";
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

  const cards = overviewSessions(sessions, wset, showStopped);
  const alive = aliveCount(cards);
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
      {cards.length === 0 ? (
        <EmptyState
          icon="dashboard"
          title={wset ? tr("ovw.empty_in_set", { set: wset.name }) : tr("ovw.empty")}
          hint={showStopped ? undefined : tr("ovw.empty_hint")}
        />
      ) : (
        <div className="ovw-grid" role="list">
          {cards.map((s) => (
            <SessionCard key={s.name} s={s} opens={sPanes.get(s.name) || []} multi={multi} beside={beside} running={running} actions={actions} />
          ))}
        </div>
      )}
    </div>
  );
}
