// SCM pane-open helpers. A commit's detail and a working-file diff open in
// their OWN pane, reusing a single pane of that kind when one is open (clicking
// commit after commit updates one detail pane instead of spawning many) — the
// old showCommit / showFileDiff behavior on the new layout store.
import { useLayoutStore } from "../../layout/store.ts";
import { allPanes } from "../../layout/ops.ts";
import type { PaneContent } from "../../layout/types.ts";

function openReusing(kind: "commit" | "wtdiff", content: PaneContent): void {
  const st = useLayoutStore.getState();
  const existing = allPanes(st.layout).find((p) => p.content.kind === kind);
  if (existing) {
    st.setPaneTarget(existing.id, { content });
    st.selectTab(existing.id);
    return;
  }
  st.openTargetInNew({ content });
}

export const openCommit = (repo: string, sha: string, path?: string): void =>
  openReusing("commit", { kind: "commit", scmRepo: repo, scmPath: path, commitSha: sha });

export const openCommitSplit = (repo: string, sha: string, path?: string): void =>
  useLayoutStore.getState().openTargetInNew({ content: { kind: "commit", scmRepo: repo, scmPath: path, commitSha: sha } }, true);

export const openFileDiff = (repo: string, path: string, staged: boolean): void =>
  openReusing("wtdiff", { kind: "wtdiff", scmRepo: repo, filePath: path, diffStaged: staged });

export const openChanges = (repo: string): void =>
  useLayoutStore.getState().openTarget({ content: { kind: "changes", scmRepo: repo } });

/** Open a working copy's Source Control view. The hand-off target for a branch that
 * is already checked out somewhere else: the answer to "I can't switch to it here"
 * is "here is where it lives". */
export const openRepoScm = (repo: string): void =>
  useLayoutStore.getState().openTarget({ content: { kind: "scm", scmRepo: repo } });
