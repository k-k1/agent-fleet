// SpacesSection — the Sessions view's Spaces section (ADR 0142 decision 9): sessions that run
// in a space (~/spaces/<folder>), grouped by space. Hidden while no session runs in one, like
// Other sessions. Which sessions belong here is the Agent's answer (Session.space, decided by
// the directory's root), never the folder name, so a space that shares a repository's name
// stays out of the repository tree.
import { memo, useId, useState } from "react";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { SessionRow } from "../sessions/SessionRow.tsx";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { shellPlaceKey, spaceGroups } from "../../lib/project.ts";
import { useActiveWorkingSet, sessionInSet } from "../../lib/workingSetsStore.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useRefIndex } from "../sessions/useRefIndex.ts";
import { useRailRoving } from "./useRailRoving.ts";
import { useUnreadSessions } from "../notifications/unread.ts";
import { spaceLabel, spacesOwner } from "../spaces/store.ts";
import { SpaceRenameModal } from "../spaces/SpaceRenameModal.tsx";
import { useSpaceNames } from "../spaces/useSpaceNames.ts";
import { TerminalChip } from "../terminals/TerminalChip.tsx";
import { useTerminalView } from "../terminals/useTerminalView.ts";
import type { Session } from "../../types/session.ts";
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
  // The rename dialog that is open: the space, and the workspace it was opened in. A dialog of
  // another workspace is not shown (it would send its folder to this one), so a tenant or member
  // switch closes it.
  const [renaming, setRenaming] = useState<{ folder: string; owner: string } | null>(null);
  const owner = spacesOwner();
  // A space session is repo-less, so a working set holds it by direct assignment only. The
  // search box matches a session's own text or the name its space is shown under.
  const wset = useActiveWorkingSet();
  // A space that only has shells keeps its row: the count chip on it is how the Sessions view
  // still leads to them. It is listed for the shells the Terminal view lists (under the working
  // set), and found by the name it is shown under, never by a shell's title.
  const { counts } = useTerminalView();
  const shellSpaces = [...counts.keys()].filter((k) => k.startsWith("shell:space:")).map((k) => k.slice("shell:space:".length));
  const base = spaceGroups(sessions);
  const known = new Set(base.map((g) => g.folder));
  const groups = [...base, ...shellSpaces.filter((f) => !known.has(f)).sort().map((folder) => ({ folder, sessions: [] as Session[] }))]
    .map((g) => {
      const label = spaceLabel(g.folder, spaces);
      const byLabel = !!nq && label.toLowerCase().includes(nq);
      const shells = counts.get(shellPlaceKey({ kind: "space", folder: g.folder })) ?? 0;
      const own = g.sessions.filter((s) => (!wset || sessionInSet(wset, s)) && (byLabel || sessionMatches(s, nq, refs)));
      return { ...g, label, sessions: own, listed: own.length > 0 || (shells > 0 && (!nq || byLabel)) };
    })
    .filter((g) => g.listed);

  const idBase = useId();
  const unreadSessions = useUnreadSessions();
  const count = groups.reduce((n, g) => n + g.sessions.length, 0);
  const unread = groups.some((g) => g.sessions.some((s) => unreadSessions.has(s.name)));

  if (groups.length === 0) return null;

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
                aria-expanded={g.sessions.length > 0 ? true : undefined}
                aria-owns={g.sessions.length > 0 ? `${idBase}-${g.folder}` : undefined}
                tabIndex={-1}
                data-rail-row=""
                title={tr("pj.space_folder_hint", { folder: g.folder })}
              >
                <Icon name="folder" />
                <span className="space-group-name">{g.label}</span>
                <TerminalChip groupKey={shellPlaceKey({ kind: "space", folder: g.folder })} />
                <button
                  type="button"
                  className="space-group-rename"
                  // Until the list names the space, the dialog would open on an empty name and
                  // saving it would clear the real one.
                  disabled={!spaces.some((x) => x.folder === g.folder)}
                  title={tr("pj.space_rename")}
                  aria-label={tr("pj.space_rename_aria", { name: g.label })}
                  onClick={(e) => {
                    e.stopPropagation();
                    setRenaming({ folder: g.folder, owner });
                  }}
                  onKeyDown={(e) => e.stopPropagation()}
                >
                  <Icon name="edit" />
                </button>
              </div>
            </div>
            {g.sessions.length > 0 && (
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
            )}
          </li>
        ))}
      </ul>
      {renaming !== null && renaming.owner === owner && (
        <SpaceRenameModal
          space={{ folder: renaming.folder, name: spaces.find((x) => x.folder === renaming.folder)?.name ?? "" }}
          owner={renaming.owner}
          onClose={() => setRenaming(null)}
        />
      )}
    </Section>
  );
});
