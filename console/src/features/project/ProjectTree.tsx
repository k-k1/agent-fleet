// ProjectTree — the rail's main block: working copies as collapsible nodes, each
// nesting its sessions (and, from P4, its files). Replaces the flat Sessions +
// Repos sections. The block header carries the repo-level actions (clone / 更新);
// nodes are ordered so a base clone and its worktrees sit adjacently (orderedRepos).
import { useEffect, useState } from "react";
import { apiJSON } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useReposStore } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { NewRepoModal } from "../repos/NewRepoModal.tsx";
import { useRepoRailContext } from "../repos/useRepoRail.ts";
import { useSessionActions } from "../sessions/useSessionActions.tsx";
import { orderedRepos } from "../../lib/project.ts";
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
  const toast = useToast();
  const ctx = useRepoRailContext();
  const actions = useSessionActions(); // one instance shared by every node's rows
  const running = ctx.running;

  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null);

  useEffect(() => {
    void refreshRepos();
  }, [refreshRepos]);

  const ordered = orderedRepos(repos);

  const doClone = async ({ remote_url, branch, name, new_branch }: { remote_url: string; branch: string; name: string; new_branch?: string }) => {
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name, new_branch: new_branch || "" });
      if (res && res.error) {
        toast("clone に失敗: " + (res.error.message || res.error));
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
      id="projects"
      title="プロジェクト"
      icon="repo"
      count={ordered.length}
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
          <Button small variant="ghost" icon="refresh" title="更新" onClick={() => void refreshRepos()}>
            更新
          </Button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="sess-list proj-tree">
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {ordered.length === 0 && !cloning && (
          <EmptyState icon="repo" title="リポジトリがありません" hint="clone するとここに並びます">
            {running && (
              <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                クローン
              </Button>
            )}
          </EmptyState>
        )}
        {ordered.map((r) => (
          <RepoNode key={r.name} r={r} ctx={ctx} actions={actions} />
        ))}
      </ul>
    </Section>
  );
}
