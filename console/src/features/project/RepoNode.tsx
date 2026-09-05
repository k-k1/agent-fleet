// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder (directly — no
// "sessions" sub-header: it only duplicated the node's own fold) and, for a
// base clone, its worktrees as child nodes. Collapsing a node hides the whole
// project — folding is how you focus on one project. The open state
// persists per folder (af-proj-<repo>). File browsing lives in the rail-bottom
// files section (FilesSection), not inside the node.
import { useEffect } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import { RepoRowConnected } from "../repos/RepoRowConnected.tsx";
import { useRepoReveal } from "../repos/store.ts";
import type { RepoRailContext } from "../repos/useRepoRail.ts";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { sessionsInFolder } from "../../lib/project.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useT } from "../../lib/i18n/index.ts";

interface RepoNodeProps {
  r: Repo;
  /** This base clone's worktrees — rendered as nested child nodes, so folding the
   * base folds the whole project. Absent/empty for worktree nodes themselves. */
  childRepos?: Repo[];
  ctx: RepoRailContext;
  actions: SessionActions;
}

export function RepoNode({ r, childRepos, ctx, actions }: RepoNodeProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const nq = normQuery(useProjectFilter((f) => f.q));
  const mine = sessionsInFolder(sessions, r.name);
  // Empty repos (no sessions anywhere under them, worktrees included) default
  // folded — an unused clone shouldn't take up rail space. The default is live
  // until the user pins a choice (see usePersistedOpen).
  const subtreeTotal =
    mine.length + (childRepos ?? []).reduce((n, c) => n + sessionsInFolder(sessions, c.name).length, 0);
  const node = usePersistedOpen(`af-proj-${r.name}`, subtreeTotal > 0);
  // Reveal-in-rail (command palette repo row): expand this node when it's the target —
  // or the base of a target worktree, so the worktree child mounts and focuses itself —
  // then scroll + focus the target's row. Keyed on the reveal counter so a repeat reveal
  // of the same repo still fires.
  const revealN = useRepoReveal((s) => s.n);
  useEffect(() => {
    const target = useRepoReveal.getState().name;
    if (!target) return;
    const isTarget = target === r.name;
    const isBaseOfTarget = (childRepos ?? []).some((c) => c.name === target);
    if (isTarget || isBaseOfTarget) node.set(true);
    if (isTarget) {
      requestAnimationFrame(() => {
        const sel = `[data-rail-repo="${typeof CSS !== "undefined" && CSS.escape ? CSS.escape(r.name) : r.name}"]`;
        const el = document.querySelector<HTMLElement>(sel);
        if (el) {
          el.scrollIntoView({ block: "nearest" });
          el.focus();
        }
      });
    }
    // node.set identity is stable enough; depend on the reveal counter only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealN]);
  // While filtering, every visible node is forced open (the parent already
  // pruned the tree to matches) and only matching sessions render.
  const open = nq ? true : node.open;
  const shownSessions = nq ? mine.filter((s) => sessionMatches(s, nq)) : mine;
  // Session tally for the repo row's badge — real counts, not the filtered view:
  // own folder while open (the rows are visible right below); the worktrees'
  // sessions fold in while collapsed, so a folded project still shows what's
  // running inside.
  let sessAlive = mine.filter((s) => s.alive).length;
  let sessTotal = mine.length;
  // The right-click "archive all stopped sessions" applies only to the sessions in this node's
  // own folder: mine comes from sessionsInFolder(r.name), so a worktree counts as a separate
  // folder name and is naturally excluded from the base repo's bulk action.
  const stoppedMine = mine.filter((s) => !s.alive && !s.locked);
  if (!open && childRepos) {
    for (const c of childRepos) {
      const cs = sessionsInFolder(sessions, c.name);
      sessAlive += cs.filter((s) => s.alive).length;
      sessTotal += cs.length;
    }
  }
  const row = (s: Session) => (
    <SessionRow
      key={s.name}
      s={s}
      selected={ctx.activeSession === s.name}
      opens={ctx.sPanes?.get(s.name) || []}
      multi={ctx.multiPane}
      running={ctx.running}
      actions={actions}
    />
  );
  return (
    <li className={"proj-node" + (open ? "" : " collapsed") + (r.worktree ? " wt" : " base")}>
      <div className="proj-node-head">
        <button
          type="button"
          className="proj-node-caret"
          onClick={node.toggle}
          aria-expanded={open}
          title={open ? tr("pj.collapse") : tr("pj.expand")}
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} />
        </button>
        <ul className="sess-list proj-node-repo">
          <RepoRowConnected
            r={r}
            ctx={ctx}
            onToggle={node.toggle}
            sess={{ alive: sessAlive, total: sessTotal }}
            stoppedCount={stoppedMine.length}
            onArchiveStopped={() => void actions.archiveStopped(stoppedMine)}
          />
        </ul>
      </div>
      {open && (
        <>
          {/* Sessions sit directly under the repo row — no sub-header, no empty
              placeholder: a repo with none simply shows nothing here. Only the
              base's own direct sessions get the gray guide line (they have no
              accent spine of their own). */}
          {shownSessions.length > 0 && (
            <div className="proj-node-body">
              <ul className="sess-list proj-sub-list">{shownSessions.map(row)}</ul>
            </div>
          )}

          {/* Worktrees as real child nodes — each carries its own accent spine,
              so they hang directly off the node (NOT wrapped in the bordered
              body) to avoid a second, redundant gray guide left of the spine.
              The spine is indented to sit where that guide used to be. */}
          {childRepos && childRepos.length > 0 && (
            <ul className="proj-children">
              {childRepos.map((c) => (
                <RepoNode key={c.name} r={c} ctx={ctx} actions={actions} />
              ))}
            </ul>
          )}
        </>
      )}
    </li>
  );
}
