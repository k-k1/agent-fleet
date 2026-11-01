// HomeView — the member's daily entry point (#2028): what waits for them and where they left off,
// with the first-run setup on top until it is done. It is what an empty layout shows (the sole
// empty cell, panes/Pane.tsx), so a new member and someone who just closed everything both land
// on a next step instead of a blank pane.
//
// It adds no ordering or state rules of its own. "Waiting" and "continue" are the command
// palette's attention order cut into two lists (home.ts), and each card is the overview's
// SessionCard, so a session reads and opens the same way here as there.
//
// The working-set filter is deliberately NOT applied: a session waiting for an answer is still
// waiting when a working set hides it from the rail, and Home exists to surface exactly that.
import type { ReactNode } from "react";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useIsMobile } from "../../lib/device.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { sessionPanes } from "../../layout/badges.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { useNotificationStore } from "../notifications/store.ts";
import { waitingAtFromNotifications } from "../sessions/order.ts";
import { homeLists } from "./home.ts";
import { observedWaitingAt } from "../sessions/waiting.ts";
import { SessionCard } from "../overview/SessionCard.tsx";
import { GlanceSection } from "./GlanceSection.tsx";
import { SetupCard, useSetupState } from "../terminal/OnboardingCard.tsx";
import { openAssistantDraft } from "../chat/open.ts";
import type { Session } from "../../types/session.ts";
import "../overview/overview.css";
import "./home.css";

// The flagship builtin assistant (workspace/agent/assistants.go: afAssistantID), the same one
// the first-run card's chat tile opens.
const AF_ASSISTANT_ID = "af";

interface HomeViewProps {
  /** Shown in the sole empty cell rather than as a pane: no pane head, and a card opens the
   *  session in place, because there is nothing to keep beside it. */
  embedded?: boolean;
  headerActions?: ReactNode;
}

export function HomeView({ embedded = false, headerActions }: HomeViewProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const openStart = useSessionsStore((s) => s.openStart);
  const running = useWorkspaceStore((s) => s.state) === "running";
  const layout = useLayoutStore((s) => s.layout);
  const actions = useSessionActions();
  const isMobile = useIsMobile();
  const beside = !embedded && !isMobile;
  const notifications = useNotificationStore((st) => st.items);
  const fromNotifications = waitingAtFromNotifications(notifications);
  const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));
  const { waiting, next } = homeLists(sessions, waitingAt);
  const sPanes = sessionPanes(layout);
  const setup = useSetupState();

  const cards = (list: Session[]) => (
    <div className="ovw-grid" role="list">
      {list.map((s) => (
        <SessionCard
          key={s.name}
          s={s}
          opens={sPanes.get(s.name) || []}
          beside={beside}
          running={running}
          waitingAt={waitingAt(s.name)}
          actions={actions}
        />
      ))}
    </div>
  );

  return (
    <div className={"home" + (embedded ? " home-embedded" : "")}>
      {!embedded && (
        <ViewHead actions={headerActions}>
          <span className="view-title">
            <Icon name="home" /> {tr("pane.kind.home")}
          </span>
        </ViewHead>
      )}
      <div className="home-body">
        <SetupCard setup={setup} />
        {waiting.length > 0 && (
          <section className="home-section home-waiting">
            <h3 className="home-title" title={tr("home.waiting_hint")}>
              <Icon name="bell-dot" /> {tr("home.waiting")}
              <span className="home-count">{waiting.length}</span>
            </h3>
            {cards(waiting)}
          </section>
        )}
        {next.length > 0 && (
          <section className="home-section">
            <h3 className="home-title">
              <Icon name="history" /> {tr("home.continue")}
            </h3>
            {cards(next)}
          </section>
        )}
        {/* Below the lists, so what is waiting stays first; it costs nothing while folded. */}
        <GlanceSection />
        {/* While the setup card shows, its goal tiles are the start actions; a second pair here
            would put two of each button on one screen. */}
        {!setup.show && (
        <section className="home-section">
          <h3 className="home-title">
            <Icon name="add" /> {tr("home.start")}
          </h3>
          {sessions.length === 0 && <p className="home-empty">{tr("home.empty")}</p>}
          <div className="home-actions">
            <Button variant="primary" icon="rocket" onClick={() => openStart()}>
              {tr("onb.get_started")}
            </Button>
            <Button
              icon="comment"
              disabled={!running}
              title={running ? undefined : tr("onb.ws_first")}
              onClick={() => openAssistantDraft(AF_ASSISTANT_ID)}
            >
              {tr("onb.start_chat")}
            </Button>
          </div>
        </section>
        )}
      </div>
    </div>
  );
}
