// SpacesSection — the Sessions view's Spaces section (ADR 0142 decision 9): sessions that run
// in a space (~/spaces/<folder>), grouped by space. Hidden while no session runs in one, like
// Other sessions. Which sessions belong here is the Agent's answer (Session.space, decided by
// the directory's root), never the folder name, so a space that shares a repository's name
// stays out of the repository tree.
import { memo, useId } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { shellPlaceKey, spaceGroups, terminalsInSpace } from "../../lib/project.ts";
import { useActiveWorkingSet, sessionInSet } from "../../lib/workingSetsStore.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useRefIndex } from "../sessions/useRefIndex.ts";
import { useRailRoving } from "./useRailRoving.ts";
import { useUnreadSessions } from "../notifications/unread.ts";
import { spaceLabel } from "../spaces/store.ts";
import { useSpaceNames } from "../spaces/useSpaceNames.ts";
import { TerminalChip } from "../terminals/TerminalChip.tsx";
import { useT } from "../../lib/i18n/index.ts";

export const SpacesSection = memo(function SpacesSection() {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const spaces = useSpaceNames(sessions);
  const ctx = useRepoRailContext();
  const actions = useSessionActions();
  const nq = normQuery(useProjectFilter((f) => f.q));
  const refs = useRefIndex();
  const rail = useRailRoving();
  // A space session is repo-less, so a working set holds it by direct assignment only. The
  // search box matches a session's own text or the name its space is shown under.
  const wset = useActiveWorkingSet();
  const groups = spaceGroups(sessions)
    .map((g) => {
      const label = spaceLabel(g.folder, spaces);
      const byLabel = !!nq && label.toLowerCase().includes(nq);
      return {
        ...g,
        label,
        sessions: g.sessions.filter((s) => (!wset || sessionInSet(wset, s)) && (byLabel || sessionMatches(s, nq, refs))),
      };
    })
    .filter((g) => g.sessions.length > 0);

  const idBase = useId();
  const unreadSessions = useUnreadSessions();
  const count = groups.reduce((n, g) => n + g.sessions.length, 0);
  const unread = groups.some((g) => g.sessions.some((s) => unreadSessions.has(s.name)));

  if (count === 0) return null;

  return (
    <Section id="spaces" title={tr("pj.spaces")} icon="folder" count={count} unread={unread}>
      <ul
        className="sess-list space-list"
        ref={rail.ref}
        role="tree"
        aria-label={tr("pj.spaces")}
        onKeyDown={rail.onKeyDown}
      >
        {groups.map((g) => (
          // Built like a repository node (RepoNode): the space's own row is the parent treeitem,
          // focusable and a rail row, so arrow keys reach it and Left from a session returns to
          // it (useRailRoving looks for .proj-node > .proj-node-head). The group sits outside the
          // row for that lookup, so aria-owns is what tells a screen reader the sessions belong
          // to this space.
          <li key={g.folder} className="proj-node space-group">
            <div className="proj-node-head space-group-head">
              <div
                className="space-group-row"
                role="treeitem"
                aria-expanded={true}
                aria-owns={`${idBase}-${g.folder}`}
                tabIndex={-1}
                data-rail-row=""
                title={tr("pj.space_folder_hint", { folder: g.folder })}
              >
                <Icon name="folder" />
                <span className="space-group-name">{g.label}</span>
                <TerminalChip count={terminalsInSpace(sessions, g.folder).length} groupKey={shellPlaceKey({ kind: "space", folder: g.folder })} />
              </div>
            </div>
            <ul id={`${idBase}-${g.folder}`} className="sess-list proj-sub-list" role="group" aria-label={g.label}>
              {g.sessions.map((s) => (
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
          </li>
        ))}
      </ul>
    </Section>
  );
});
