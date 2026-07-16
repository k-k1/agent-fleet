// Workspace-stopped rail: session metadata remains available from the Control
// Plane, but repo metadata and mutating session operations require the Agent.
// Show every session as one history list instead of misclassifying all of them
// as "その他" merely because the repo endpoint is unavailable.
import { Section } from "../../ui/Section.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { paneCount, sessionPanes } from "../../layout/badges.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function StoppedSessionsSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const multi = paneCount(layout) > 1;
  const panes = multi ? sessionPanes(layout) : null;
  const selected = activePane(layout)?.session ?? null;

  if (sessions.length === 0) return null;

  return (
    <Section id="stopped-sessions" title={tr("pj.session_history")} icon="history" count={sessions.length}>
      <ul className="sess-list">
        {sessions.map((s) => (
          <SessionRow
            key={s.name}
            s={s}
            selected={selected === s.name}
            opens={panes?.get(s.name) || []}
            multi={multi}
            running={false}
            readOnly
          />
        ))}
      </ul>
    </Section>
  );
}
