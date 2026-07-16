// OtherSessionsSection — the catch-all for sessions that belong to no working copy
// (a shell in home, or a session whose repo was removed). Hidden entirely when
// there are none, so it doesn't float empty at the foot of the rail. The global
// session-maintenance actions (整理 / アーカイブ) moved to the プロジェクト header.
import { Section } from "../../ui/Section.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useReposStore } from "../repos/store.ts";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { orphanSessions } from "../../lib/project.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useRailRoving } from "./useRailRoving.ts";

export function OtherSessionsSection() {
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const ctx = useRepoRailContext();
  const actions = useSessionActions();
  const nq = normQuery(useProjectFilter((f) => f.q));
  const rail = useRailRoving();
  // The rail filter (the ProjectTree search box) narrows this list too.
  const orphans = orphanSessions(sessions, repos).filter((s) => sessionMatches(s, nq));

  // Nothing loose → no section at all (keeps the rail's foot clean).
  if (orphans.length === 0) return null;

  return (
    <Section
      id="other-sessions"
      title="その他のセッション"
      icon="terminal"
      count={orphans.length}
      actions={
        <IconButton
          icon="trash"
          label="その他のセッションをすべて削除"
          onClick={() => void actions.deleteOrphans(orphans)}
        />
      }
    >
      <ul className="sess-list" ref={rail.ref} role="tree" onKeyDown={rail.onKeyDown}>
        {orphans.map((s) => (
          <SessionRow
            key={s.name}
            s={s}
            selected={ctx.activeSession === s.name}
            opens={ctx.sPanes?.get(s.name) || []}
            multi={ctx.multiPane}
            running={ctx.running}
            actions={actions}
          />
        ))}
      </ul>
    </Section>
  );
}
