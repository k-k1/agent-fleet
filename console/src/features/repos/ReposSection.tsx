// ReposSection — the flat working-copies list: clone (provider picker / URL) and
// 更新 in the header, one RepoRow per repo. The row itself (launch / branch / FF /
// delete / open-SCM) is RepoRowConnected, wired from the stores; the rail-level
// derivations (launch kinds, active/scm highlight, ordinal badges) come from
// useRepoRailContext. Both are reused by the project tree's working-copy nodes.
import { useEffect, useState } from "react";
import { apiJSON } from "../../core/api/client.ts";
import { Section } from "../../ui/Section.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";
import { NewRepoModal } from "./NewRepoModal.tsx";
import { RepoRowConnected } from "./RepoRowConnected.tsx";
import { useRepoRailContext } from "./useRepoRail.ts";

// guessRepoName derives a display name from a clone URL for the in-progress
// spinner row, before the server reports the real name.
const guessRepoName = (u: string | null | undefined) => {
  const s = String(u || "").replace(/\.git$/, "").replace(/\/+$/, "");
  return s.split(/[/:]/).pop() || "repo";
};

export function ReposSection() {
  const repos = useReposStore((s) => s.repos);
  const refreshRepos = useReposStore((s) => s.refresh);
  const toast = useToast();
  const ctx = useRepoRailContext();
  const running = ctx.running;

  const [showClone, setShowClone] = useState(false);
  const [cloning, setCloning] = useState<{ name: string } | null>(null);

  useEffect(() => {
    void refreshRepos();
  }, [refreshRepos]);

  // Run a clone in the background: the modal closed already, so progress shows
  // as a spinner row here until the server finishes.
  const doClone = async ({ remote_url, branch, name, new_branch }: { remote_url: string; branch: string; name: string; new_branch?: string }) => {
    setCloning({ name: name || guessRepoName(remote_url) });
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url, branch, name, new_branch: new_branch || "" });
      if (res && res.error) {
        toast("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      void refreshRepos();
      // Clone finished server-side — reveal the new working copy in the Files tree.
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
      title="Repos"
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
          <Button small variant="ghost" icon="refresh" title="更新" onClick={() => void refreshRepos()}>
            更新
          </Button>
        </>
      }
    >
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="sess-list">
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {repos.length === 0 && !cloning && (
          <EmptyState icon="repo" title="リポジトリがありません" hint="clone するとここに並びます">
            {running && (
              <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                クローン
              </Button>
            )}
          </EmptyState>
        )}
        {repos.map((r) => (
          <RepoRowConnected key={r.name} r={r} ctx={ctx} />
        ))}
      </ul>
    </Section>
  );
}
