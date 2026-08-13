// OtherSessionsSection — the catch-all for sessions that belong to no working copy
// (a shell in home, or a session whose repo was removed). Hidden entirely when
// there are none, so it doesn't float empty at the foot of the rail. The global
// session-maintenance actions (整理 / アーカイブ) moved to the プロジェクト header.
import { memo } from "react";
import { Section } from "../../ui/Section.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useReposStore } from "../repos/store.ts";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { orphanSessions } from "../../lib/project.ts";
import { useActiveWorkingSet, sessionInSet } from "../../lib/workingSetsStore.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useRailRoving } from "./useRailRoving.ts";
import { useT } from "../../lib/i18n/index.ts";

export const OtherSessionsSection = memo(function OtherSessionsSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const repos = useReposStore((s) => s.repos);
  const ctx = useRepoRailContext();
  const actions = useSessionActions();
  const nq = normQuery(useProjectFilter((f) => f.q));
  const rail = useRailRoving();
  // 作業グループ (docs/52) narrows this list — direct assignment (set.sessions) or
  // folder-name inheritance (covers a session whose repo was deleted) — then the
  // rail filter (the ProjectTree search box) narrows it further.
  const wset = useActiveWorkingSet();
  const orphans = orphanSessions(sessions, repos)
    .filter((s) => !wset || sessionInSet(wset, s))
    .filter((s) => sessionMatches(s, nq));

  // Nothing loose → no section at all (keeps the rail's foot clean).
  if (orphans.length === 0) return null;

  return (
    <Section
      id="other-sessions"
      title={tr("pj.other_sessions")}
      icon="terminal"
      count={orphans.length}
      actions={
        <IconButton
          icon="archive"
          label={tr("pj.tidy_other_sessions")}
          onClick={() => void actions.clearOrphans(orphans)}
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
});
