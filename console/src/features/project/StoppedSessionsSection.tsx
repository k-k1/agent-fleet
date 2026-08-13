// Workspace-stopped rail: session metadata remains available from the Control
// Plane, but repo metadata and mutating session operations require the Agent.
// Show every session as one history list instead of misclassifying all of them
// as "その他" merely because the repo endpoint is unavailable.
import { memo } from "react";
import { Section } from "../../ui/Section.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { paneCount, sessionPanes } from "../../layout/badges.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useActiveWorkingSet, sessionInSet } from "../../lib/workingSetsStore.ts";
import { useT } from "../../lib/i18n/index.ts";

export const StoppedSessionsSection = memo(function StoppedSessionsSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const multi = paneCount(layout) > 1;
  const panes = multi ? sessionPanes(layout) : null;
  const selected = activePane(layout)?.session ?? null;
  // 作業グループ (docs/52): membership resolves from the folder name alone, so the
  // scope keeps working here even though the repo list needs the (stopped) agent.
  const wset = useActiveWorkingSet();
  const shown = wset ? sessions.filter((s) => sessionInSet(wset, s)) : sessions;

  if (shown.length === 0) return null;

  return (
    <Section id="stopped-sessions" title={tr("pj.session_history")} icon="history" count={shown.length}>
      <ul className="sess-list">
        {shown.map((s) => (
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
});
