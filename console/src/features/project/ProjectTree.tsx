// ProjectTree — the rail's main block: the リポジトリ section (same header band
// as アシスタント / ファイル), whose body lays repos out directly (no group
// bands). Each base clone is a top-level collapsible node and its worktrees nest
// as child nodes inside it, so a project reads as real hierarchy instead of a
// decorated flat list — and folding the base folds the whole project. The
// section header carries the repo actions (clone / 更新) and the
// session-maintenance actions (整理 / アーカイブ).
import { useEffect, useState } from "react";
import { apiJSON, errText } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button, IconButton } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { NewRepoModal } from "../repos/NewRepoModal.tsx";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSessionUI } from "../sessions/ui.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { groupedRepos, sessionsInFolder } from "../../lib/project.ts";
import { useProjectFilter, normQuery, repoMatches, sessionMatches } from "./filter.ts";
import { RepoNode } from "./RepoNode.tsx";

// guessRepoName derives a display name from a clone URL for the in-progress
// spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

export function ProjectTree() {
  const repos = useReposStore((s) => s.repos);
  const refreshRepos = useReposStore((s) => s.refresh);
  const sessions = useSessionsStore((s) => s.sessions);
  const openArchived = useSessionUI((u) => u.openArchived);
  const toast = useToast();
  const ctx = useRepoRailContext();
  const actions = useSessionActions(); // one instance shared by every node's rows
  const running = ctx.running;

  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null);
  const q = useProjectFilter((f) => f.q);
  const setQ = useProjectFilter((f) => f.setQ);
  const nq = normQuery(q);

  useEffect(() => {
    void refreshRepos();
  }, [refreshRepos]);

  // Filtering: a working copy is visible when it matches itself or hosts a
  // matching session; a base also stays as the anchor of a matching worktree.
  // While filtering, only the visible worktrees are passed down as children.
  const visible = (r: (typeof repos)[number]) =>
    repoMatches(r, nq) || sessionsInFolder(sessions, r.name).some((s) => sessionMatches(s, nq));
  const groups = groupedRepos(repos)
    .filter((g) => !nq || g.some(visible))
    .map((g) => (nq ? [g[0], ...g.slice(1).filter(visible)] : g));

  const doClone = async ({ remote_url, branch, name, new_branch }: { remote_url: string; branch: string; name: string; new_branch?: string }) => {
    // Snapshot the existing folders so we can tell an actually-new working copy
    // apart from the ones already here, if we have to re-check after an error.
    const beforeNames = new Set(useReposStore.getState().repos.map((r) => r.name));
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name, new_branch: new_branch || "" });
      if (res && res.error) {
        // A big clone can outlive an upstream proxy timeout: the server keeps
        // cloning (it doesn't watch the request context) and usually finishes,
        // but the browser gets an empty/gateway response that surfaces here as an
        // error. Re-check the repo list — if a new working copy appeared, the
        // clone actually succeeded and we should reveal it, not cry failure.
        await refreshRepos();
        const added = useReposStore.getState().repos.find((r) => !beforeNames.has(r.name));
        if (!added) {
          toast("clone に失敗: " + errText(res.error));
          return;
        }
        useFilesStore.getState().revealInFiles("repos/" + added.name);
        return;
      }
      void refreshRepos();
      if (res && res.name) useFilesStore.getState().revealInFiles("repos/" + res.name);
      else useFilesStore.getState().bump();
    } catch (e) {
      toast("clone に失敗: " + e);
    } finally {
      setCloning(null);
    }
  };

  return (
    <Section
      id="repos"
      title="リポジトリ"
      icon="repo"
      count={repos.length}
      actions={
        <>
          <Button
            small
            variant="ghost"
            icon="add"
            title={running ? "clone" : "clone（ワークスペース停止中）"}
            disabled={!!cloning || !running}
            onClick={() => setShowClone((s) => !s)}
          >
            クローン
          </Button>
          <IconButton icon="refresh" label="更新" onClick={() => void refreshRepos()} />
          <span className="proj-head-sep" aria-hidden="true" />
          <IconButton
            icon="clear-all"
            label="停止中をまとめてアーカイブ（shell/ssm は削除）"
            disabled={!sessions.some((s) => !s.alive)}
            onClick={actions.clearStopped}
          />
          <IconButton icon="archive" label="アーカイブを開く（復帰）" onClick={openArchived} />
        </>
      }
    >
      {/* Quick filter: narrows repos + sessions (この木 and その他のセッション).
          Escape clears. Files are untouched — the tree below is lazy-loaded. */}
      <div className="proj-filter">
        <Icon name="search" />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Escape" && setQ("")}
          placeholder="絞り込み（リポ / セッション）"
          aria-label="リポジトリとセッションを絞り込み"
        />
        {q && (
          <button type="button" className="proj-filter-clear" title="クリア" onClick={() => setQ("")}>
            <Icon name="close" />
          </button>
        )}
      </div>
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="sess-list proj-tree">
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {groups.length === 0 && !cloning && (
          nq ? (
            <li className="proj-sub-empty">「{q.trim()}」に一致するリポ・セッションはありません</li>
          ) : (
            <EmptyState icon="repo" title="リポジトリがありません" hint="clone するとここに並びます">
              {running && (
                <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                  クローン
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
}
