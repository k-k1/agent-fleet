// ProjectTree — the rail's main block: working copies as collapsible nodes,
// clustered into project groups (a base clone + its worktrees) so each project
// reads as one visual unit, separated from the next. Replaces the flat Sessions +
// Repos sections. The block header carries repo actions (clone / 更新) plus the
// global session-maintenance actions (整理 / アーカイブ) that used to float on the
// orphan section's header.
import { useEffect, useState } from "react";
import { apiJSON } from "../../core/api/client.ts";
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
import { groupedRepos } from "../../lib/project.ts";
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

  useEffect(() => {
    void refreshRepos();
  }, [refreshRepos]);

  const groups = groupedRepos(repos);

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
      {showClone && <NewRepoModal onClose={() => setShowClone(false)} onClone={doClone} repos={repos} />}
      <ul className="sess-list proj-tree">
        {cloning && (
          <li className="repo-cloning">
            <Icon name="loading" spin /> Cloning {cloning.name}…
          </li>
        )}
        {groups.length === 0 && !cloning && (
          <EmptyState icon="repo" title="リポジトリがありません" hint="clone するとここに並びます">
            {running && (
              <Button small variant="primary" icon="add" onClick={() => setShowClone(true)}>
                クローン
              </Button>
            )}
          </EmptyState>
        )}
        {groups.map((members) => (
          <li key={members[0].name} className="proj-group">
            <ul className="proj-group-list">
              {members.map((r) => (
                <RepoNode key={r.name} r={r} ctx={ctx} actions={actions} />
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </Section>
  );
}
