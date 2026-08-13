// ProjectTree — the rail's main block: the リポジトリ section (same header band
// as アシスタント / ファイル), whose body lays repos out directly (no group
// bands). Each base clone is a top-level collapsible node and its worktrees nest
// as child nodes inside it, so a project reads as real hierarchy instead of a
// decorated flat list — and folding the base folds the whole project. The
// section header carries the repo actions (clone / 更新) and the
// session-maintenance actions (整理 / アーカイブ).
import { memo, useState } from "react";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useReposStore, useLaunchTarget } from "../repos/store.ts";
import { NewRepoModal } from "../repos/NewRepoModal.tsx";
import { cloneRepo, svnCheckout } from "../repos/clone.ts";
import type { CloneRequest, SvnCheckoutRequest } from "../repos/clone.ts";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { groupedRepos, sessionsInFolder } from "../../lib/project.ts";
import { useActiveWorkingSet, repoInSet, autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useProjectFilter, normQuery, repoMatches, sessionMatches } from "./filter.ts";
import { RepoNode } from "./RepoNode.tsx";
import { useRailRoving } from "./useRailRoving.ts";
import { useT } from "../../lib/i18n/index.ts";
import { ShareManagerModal } from "../sharing/ShareManagerModal.tsx";

// guessRepoName derives a display name from a clone URL for the in-progress
// spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

export const ProjectTree = memo(function ProjectTree() {
  const tr = useT();
  const repos = useReposStore((s) => s.repos);
  const refreshRepos = useReposStore((s) => s.refresh);
  const clearRepos = useReposStore((s) => s.clear);
  const sessions = useSessionsStore((s) => s.sessions);
  const openArchived = useSessionUI((u) => u.openArchived);
  const openCleanup = useSessionUI((u) => u.openCleanup);
  const toast = useToast();
  const ctx = useRepoRailContext();
  const actions = useSessionActions(); // one instance shared by every node's rows
  const running = ctx.running;

  const [showClone, setShowClone] = useState(false);
  const [showShares, setShowShares] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null);
  const q = useProjectFilter((f) => f.q);
  const setQ = useProjectFilter((f) => f.setQ);
  const nq = normQuery(q);
  const rail = useRailRoving();

  // WS 起動直後は agent がまだ不通で、CP が GET /api/repos にプレーンテキストの 502 を返す。
  // store の refresh() はこれを過渡的失敗として repos を保持したまま false を返すので、running
  // 中はバックオフ再試行して agent 復帰を待つ（さもないと 60s ポーリングが来るまで
  // リポジトリがありません のまま固着し、セッションは全て その他のセッション に落ちる）。
  // 停止中の WS も同じ 502 を返す＝両者は running でしか判別できないため、停止中は空を確定して
  // 無駄な再試行を避ける（ProjectFiles と同じ形）。
  useRetryLoad(
    async (signal) => {
      const settled = await refreshRepos();
      if (signal.aborted) return true;
      if (settled) return true;
      if (running) return false; // agent still booting — retry
      clearRepos(); // stopped WS — settle to empty
      return true;
    },
    [refreshRepos, clearRepos, running],
  );

  // 作業グループ (docs/52): scope to the active group first — a whole project
  // (base + its worktrees) is in or out by the base's membership.
  const wset = useActiveWorkingSet();
  const scoped = groupedRepos(repos).filter((g) => !wset || repoInSet(wset, g[0]));
  // Filtering: a working copy is visible when it matches itself or hosts a
  // matching session; a base also stays as the anchor of a matching worktree.
  // While filtering, only the visible worktrees are passed down as children.
  const visible = (r: (typeof repos)[number]) =>
    repoMatches(r, nq) || sessionsInFolder(sessions, r.name).some((s) => sessionMatches(s, nq));
  const groups = scoped
    .filter((g) => !nq || g.some(visible))
    .map((g) => (nq ? [g[0], ...g.slice(1).filter(visible)] : g));

  const doClone = async (req: CloneRequest) => {
    setCloning({ name: req.name || guessRepoName(req.remote_url) });
    try {
      const res = await cloneRepo(req, toast); // proxy-timeout re-check + reveal live in clone.ts
      // グループ選択中の clone はそのグループへ自動所属（docs/52 §1 — さもないと
      // 作った直後に絞り込みで見えなくなる）。
      if (res.ok && res.name) autoAddToActiveWorkingSet("repos", res.name);
      // clone-only path: bridge straight into 作業を始める (起動導線 Ph3) so
      // "clone してから起動" doesn't require hunting for the row's 起動 button.
      const repo = res.ok && res.name ? useReposStore.getState().repos.find((r) => r.name === res.name) : undefined;
      if (repo) {
        toast(
          <span className="clone-done-toast">
            {tr("pj.cloned", { name: repo.name })}
            <Button small icon="play" onClick={() => useLaunchTarget.getState().open(repo)}>
              {tr("pj.start_now")}
            </Button>
          </span>,
          { kind: "success", duration: 10000 },
        );
      }
    } finally {
      setCloning(null);
    }
  };

  const doSvnCheckout = async (req: SvnCheckoutRequest) => {
    setCloning({ name: req.name || guessRepoName(req.url) });
    try {
      const res = await svnCheckout(req, toast); // proxy-timeout re-check + reveal live in clone.ts
      if (res.ok && res.name) autoAddToActiveWorkingSet("repos", res.name); // docs/52 §1
      const repo = res.ok && res.name ? useReposStore.getState().repos.find((r) => r.name === res.name) : undefined;
      if (repo) {
        toast(
          <span className="clone-done-toast">
            {tr("pj.cloned", { name: repo.name })}
            <Button small icon="play" onClick={() => useLaunchTarget.getState().open(repo)}>
              {tr("pj.start_now")}
            </Button>
          </span>,
          { kind: "success", duration: 10000 },
        );
      }
    } finally {
      setCloning(null);
    }
  };

  return (
    <Section
      id="repos"
      title={tr("pj.repos")}
      icon="repo"
      count={wset ? scoped.reduce((n, g) => n + g.length, 0) : repos.length}
      actions={
        <>
          <Button
            small
            variant="ghost"
            icon="add"
            title={running ? tr("pj.clone") : tr("pj.clone_ws_stopped")}
            disabled={!!cloning || !running}
            onClick={() => setShowClone((s) => !s)}
          >
            {tr("pj.clone")}
          </Button>
          <IconButton icon="refresh" label={tr("pj.refresh")} onClick={() => void refreshRepos()} />
          <IconButton icon="broadcast" label={tr("share.manage_title")} onClick={() => setShowShares(true)} />
          <span className="proj-head-sep" aria-hidden="true" />
          <IconButton icon="trash" label={tr("clean.open")} onClick={openCleanup} />
          <IconButton icon="archive" label={tr("pj.open_archive")} onClick={openArchived} />
        </>
      }
    >
      {/* Quick filter: narrows repos + sessions (この木 and その他のセッション).
          Escape clears. Files are untouched — the tree below is lazy-loaded. */}
      <div className="proj-filter-bar">
        <div className="proj-filter">
          <Icon name="search" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setQ("");
              else if (e.key === "Enter") {
                e.preventDefault();
                rail.focusFirst();
              }
            }}
            placeholder={tr("pj.filter_repos_ph")}
            aria-label={tr("pj.filter_repos_aria")}
          />
          {q && (
            <button type="button" className="proj-filter-clear" title={tr("pj.clear")} onClick={() => setQ("")}>
              <Icon name="close" />
            </button>
          )}
        </div>
      </div>
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} onSvnCheckout={doSvnCheckout} repos={repos} />}
      {showShares && <ShareManagerModal onClose={() => setShowShares(false)} />}
      <ul className="sess-list proj-tree" ref={rail.ref} role="tree" onKeyDown={rail.onKeyDown}>
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> {tr("pj.cloning", { name: cloning.name })}
          </li>
        )}
        {groups.length === 0 && !cloning && (
          nq ? (
            <li className="proj-sub-empty">{tr("pj.no_match", { q: q.trim() })}</li>
          ) : wset && repos.length > 0 ? (
            // グループで絞った結果の空（リポジトリ自体はある）— 行メニューから
            // 割り当てられることを示す。真の空（clone 前）とは区別する。
            <li className="proj-sub-empty">{tr("wset.no_repos")}</li>
          ) : (
            <EmptyState icon="repo" title={tr("pj.no_repos")} hint={tr("pj.no_repos_hint")}>
              {running && (
                <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                  {tr("pj.clone")}
                </Button>
              )}
            </EmptyState>
          )
        )}
        {/* One top-level node per base clone; its worktrees nest inside as child
            nodes (an orphaned worktree group has the worktree itself at [0]). */}
        {groups.map((members) => (
          <RepoNode key={members[0].name} r={members[0]} childRepos={members.slice(1)} ctx={ctx} actions={actions} />
        ))}
      </ul>
    </Section>
  );
});
