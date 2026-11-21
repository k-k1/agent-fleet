// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder (directly — no
// "sessions" sub-header: it only duplicated the node's own fold) and the worktrees
// that hang off it. Collapsing a node hides the whole subtree — folding is how you
// focus on one project. The open state persists per folder (af-proj-<repo>). File
// browsing lives in the rail-bottom files section (FilesSection), not inside the node.
//
// The node RECURSES: a worktree's own worktrees (a child session's copy, and its
// child's) nest one level further, so a spawn chain reads as a chain. Indentation
// stops at three levels — a handoff chain has no bound and the rail is narrow — and
// past that the spine colour alone carries the relation.
import { useEffect, useMemo } from "react";
import type { ReactElement } from "react";
import type { CSSProperties } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import { RepoRowConnected } from "../repos/RepoRowConnected.tsx";
import { useRepoReveal } from "../repos/store.ts";
import type { RepoRailContext } from "../repos/useRepoRail.ts";
import type { Session } from "../../types/session.ts";
import { displayName } from "../../lib/sessionview.ts";
import { AUTO_GROUP_KINDS, childNodes, splitNodeSessions, sessionsInFolder, withoutAutoRun } from "../../lib/project.ts";
import type { AutoGroupKind, RepoTreeNode } from "../../lib/project.ts";
import { usePersistedOpen } from "../../lib/usePersistedOpen.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";
import { useRefIndex } from "../sessions/useRefIndex.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useUnreadSessions } from "../notifications/unread.ts";
import { usePublishedHeight } from "../../lib/usePublishedHeight.ts";

/** Deepest level that still adds indentation; below it nodes stay at this inset. */
const MAX_INDENT_DEPTH = 3;

/** Folder names of every working copy below this node (its own excluded). */
const descendantFolders = (n: RepoTreeNode): string[] =>
  childNodes(n).flatMap((c) => [c.repo.name, ...descendantFolders(c)]);

interface RepoNodeProps {
  node: RepoTreeNode;
  /** 0 for a group's root; each nested worktree adds one. */
  depth: number;
  ctx: RepoRailContext;
  actions: SessionActions;
  /** True below an Auto-run / Schedules group: everything in here is already apart. */
  grouped?: boolean;
}

const GROUP_KINDS = AUTO_GROUP_KINDS;

export function RepoNode({ node: n, depth, ctx, actions, grouped = false }: RepoNodeProps) {
  const r = n.repo;
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const nq = normQuery(useProjectFilter((f) => f.q));
  const refs = useRefIndex();
  // What the tree draws: a session an auto-run started is the Auto-run view's, so it is in no
  // count, preview or match here (stop / delete read the full list through `detached`).
  const shown = useMemo(() => withoutAutoRun(sessions), [sessions]);
  const mine = sessionsInFolder(shown, r.name);
  const below = descendantFolders(n);
  // Empty repos (no sessions anywhere under them, worktrees included) default
  // folded — an unused clone shouldn't take up rail space. The default is live
  // until the user pins a choice (see usePersistedOpen).
  const subtreeTotal = mine.length + below.reduce((c, f) => c + sessionsInFolder(shown, f).length, 0);
  const openState = usePersistedOpen(`af-proj-${r.name}`, subtreeTotal > 0);
  // Reveal-in-rail (command palette repo row): expand this node when it's the target —
  // or an ancestor of a target worktree, so the chain down to it mounts and the target
  // focuses itself — then scroll + focus the target's row. Keyed on the reveal counter
  // so a repeat reveal of the same repo still fires.
  const revealN = useRepoReveal((s) => s.n);
  useEffect(() => {
    const target = useRepoReveal.getState().name;
    if (!target) return;
    const isTarget = target === r.name;
    if (isTarget || below.includes(target)) openState.set(true);
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
    // openState.set identity is stable enough; depend on the reveal counter only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealN]);
  // While filtering, every visible node is forced open (the parent already
  // pruned the tree to matches) and only matching sessions render.
  const open = nq ? true : openState.open;
  // Sessions a schedule started in THIS folder (it reuses an existing copy instead of making a
  // worktree) fold into the group; an auto-run's are left to the Auto-run view. Below a group
  // nothing splits.
  const byName = useMemo(() => new Map(sessions.map((s) => [s.name, s])), [sessions]);
  const split = splitNodeSessions(mine, byName, grouped);
  const ownSessions = split.own;
  const shownSessions = nq ? ownSessions.filter((s) => sessionMatches(s, nq, refs)) : ownSessions;
  // Session tally for the repo row's badge — real counts, not the filtered view:
  // own folder while open (the rows are visible right below); the whole subtree folds
  // in while collapsed, so a folded project still shows what's running inside.
  let sessAlive = mine.filter((s) => s.alive).length;
  let sessTotal = mine.length;
  // The right-click "archive all stopped sessions" applies only to the sessions in this node's
  // own folder: mine comes from sessionsInFolder(r.name), so a worktree counts as a separate
  // folder name and is naturally excluded from the base repo's bulk action.
  const stoppedMine = mine.filter((s) => !s.alive && !s.locked);
  if (!open) {
    for (const f of below) {
      const cs = sessionsInFolder(shown, f);
      sessAlive += cs.filter((s) => s.alive).length;
      sessTotal += cs.length;
    }
  }
  // Unread dot, folded in the same way and for the same reason as the tally: while the node
  // is collapsed its row is the only thing on screen for every session underneath, so a dot
  // down there has to surface here or it is simply invisible. While open the rows carry
  // their own and repeating it on the head would say nothing new.
  const unreadSessions = useUnreadSessions();
  const unread =
    !open &&
    [...mine, ...below.flatMap((f) => sessionsInFolder(shown, f))].some((s) => unreadSessions.has(s.name));
  // A folded node summarises its own sessions on the repo row: the newest live one, else the
  // newest stopped one (shown muted), plus the names of the other live ones for the badge tooltip.
  // The summarised row is the own folder's; the "also running" names cover the whole subtree so
  // the list adds up to the badge, which folds the descendants in. `mine` is newest-first like
  // the open list; filtering forces every node open, so the summary never competes with filtered rows.
  const live = mine.filter((s) => s.alive);
  const top = live[0] ?? mine[0];
  const liveBelow = below.flatMap((f) => sessionsInFolder(shown, f).filter((s) => s.alive));
  const others = [...live.slice(1), ...liveBelow].map(displayName);
  const peek = !open && top ? { s: top } : undefined;
  // Independent of `peek`: a parent with no sessions of its own has no summary row but its badge
  // still counts the live descendants.
  const alsoRunning = !open ? others : undefined;
  // A root's header height sets where its worktrees' headers pin (project.css). Only roots:
  // deeper worktree headers do not pin, and a nested value would shadow the root's.
  const headRef = usePublishedHeight<HTMLDivElement>("--proj-base-head-h", depth === 0);
  const groupKinds = GROUP_KINDS.filter((k) => {
    if (n.groups.some((g) => g.kind === k)) return true;
    const own = split.byKind[k];
    return nq ? own.some((s) => sessionMatches(s, nq, refs)) : own.length > 0;
  });
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
    <li
      className={
        "proj-node" +
        (open ? "" : " collapsed") +
        (r.worktree ? " wt" : " base") +
        (n.spine ? " lineage" : "")
      }
      // The spine reads this; a node without a family leaves the default accent alone.
      style={n.spine ? ({ "--proj-lineage": n.spine } as CSSProperties) : undefined}
    >
      <div ref={headRef} className="proj-node-head">
        <button
          type="button"
          className="proj-node-caret"
          onClick={openState.toggle}
          aria-expanded={open}
          title={open ? tr("pj.collapse") : tr("pj.expand")}
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} />
        </button>
        <ul className="sess-list proj-node-repo">
          <RepoRowConnected
            r={r}
            ctx={ctx}
            node={n}
            onToggle={openState.toggle}
            sess={{ alive: sessAlive, total: sessTotal }}
            unread={unread}
            peek={peek}
            alsoRunning={alsoRunning}
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
              The spine is indented to sit where that guide used to be, and stops
              being indented once the chain is deeper than MAX_INDENT_DEPTH. */}
          {(n.children.length > 0 || groupKinds.length > 0) && (
            <ul className={"proj-children" + (depth >= MAX_INDENT_DEPTH ? " flat" : "")}>
              {n.children.map((c) => (
                <RepoNode key={c.repo.name} node={c} depth={depth + 1} ctx={ctx} actions={actions} grouped={grouped} />
              ))}
              {groupKinds.map((k) => (
                <AutoGroup
                  key={k}
                  kind={k}
                  owner={r.name}
                  nodes={n.groups.find((g) => g.kind === k)?.nodes ?? []}
                  sessions={split.byKind[k]}
                  depth={depth}
                  ctx={ctx}
                  actions={actions}
                  row={row}
                />
              ))}
            </ul>
          )}
        </>
      )}
    </li>
  );
}

interface AutoGroupProps {
  kind: AutoGroupKind;
  /** Folder of the node the group hangs under — part of the fold key. */
  owner: string;
  /** Unattended copies of the owner, each with its nested descendants. */
  nodes: RepoTreeNode[];
  /** Sessions in the owner's own folder that an unattended source started. */
  sessions: Session[];
  depth: number;
  ctx: RepoRailContext;
  actions: SessionActions;
  row: (s: Session) => ReactElement;
}

// AutoGroup — the folded "Auto-run" / "Schedules" bucket under a node: what unattended work
// created (copies, lib/project.ts sessionAutoKind) or ran in the node's own folder, kept apart
// from the member's own. Folded by default; the choice persists per owner and kind. Filtering
// forces it open (the tree is already pruned to matches), and a reveal aimed at a copy inside
// opens it. The header is a rail row (data-rail-row) so the arrow keys reach it, and it carries
// the unread dot of everything inside while folded, like a folded node does.
function AutoGroup({ kind, owner, nodes, sessions: own, depth, ctx, actions, row }: AutoGroupProps) {
  const tr = useT();
  const sessions = useSessionsStore((s) => s.sessions);
  const nq = normQuery(useProjectFilter((f) => f.q));
  const refs = useRefIndex();
  const unreadSessions = useUnreadSessions();
  const openState = usePersistedOpen(`af-proj-${owner}-grp-${kind}`, false);
  const folders = nodes.flatMap((c) => [c.repo.name, ...descendantFolders(c)]);
  const revealN = useRepoReveal((s) => s.n);
  useEffect(() => {
    const target = useRepoReveal.getState().name;
    if (target && folders.includes(target)) openState.set(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealN]);
  const open = nq ? true : openState.open;
  const shown = nq ? own.filter((s) => sessionMatches(s, nq, refs)) : own;
  const inside = folders.flatMap((f) => sessionsInFolder(sessions, f));
  const total = own.length + inside.length;
  const running = [...own, ...inside].filter((s) => s.alive).length;
  const unread = !open && [...own, ...inside].some((s) => unreadSessions.has(s.name));
  const label = tr(kind === "autorun" ? "pj.group_autorun" : "pj.group_schedule");
  return (
    <li className={"proj-group" + (open ? "" : " collapsed")} data-proj-group={kind}>
      <button
        type="button"
        className="proj-group-head"
        data-rail-row=""
        data-proj-group-head=""
        onClick={openState.toggle}
        aria-expanded={open}
        title={open ? tr("pj.collapse") : tr("pj.expand")}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        <span className="proj-group-label">{label}</span>
        {unread && (
          <span className="unread-dot" role="img" aria-label={tr("noti.unread_session")} title={tr("noti.unread_session")} />
        )}
        <span className="repo-sess-badge" title={tr("pj.group_count", { n: total })}>
          {total}
        </span>
        {running > 0 && (
          <span className="repo-sess-badge run" title={tr("repo.sess_running", { n: running })}>
            ●{running}
          </span>
        )}
      </button>
      {open && shown.length > 0 && <ul className="sess-list proj-sub-list">{shown.map(row)}</ul>}
      {open && nodes.length > 0 && (
        <ul className={"proj-children" + (depth + 1 >= MAX_INDENT_DEPTH ? " flat" : "")}>
          {nodes.map((c) => (
            <RepoNode key={c.repo.name} node={c} depth={depth + 1} ctx={ctx} actions={actions} grouped />
          ))}
        </ul>
      )}
    </li>
  );
}
