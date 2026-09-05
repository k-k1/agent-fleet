// WorkingCopyLabel — the heading a list of files uses to say WHICH working copy
// they came from: project + branch, not the folder name. A worktree folder is
// "<base>@<slug>" (git.go), so a group band titled with it reads as a pile of
// slugs; the project says what you are looking at and the branch says which line
// of work — the same handle the rail's repo rows already use for a worktree.
// Shared by the files section's changes view and its recursive-search group bands
// so both name a working copy the same way.
//
// Falls back to the folder name when the branch is unknown — an SVN checkout has
// none, and a folder the repos store does not know (deleted, or not loaded yet)
// reports nothing. Callers put the folder name on the band's own title.
import { Icon } from "../../ui/Icon.tsx";
import { useReposStore } from "../repos/store.ts";
import { workingCopyLabel } from "../../lib/project.ts";

export function WorkingCopyLabel({ folder }: { folder: string }) {
  const repo = useReposStore((s) => s.repos.find((r) => r.name === folder));
  const { project, branch } = workingCopyLabel(folder, repo);
  if (!branch) return <span className="wc-project">{folder}</span>;
  return (
    <>
      <span className="wc-project">{project}</span>
      {/* The same git-branch icon the rail gives a worktree row, so the second
          half reads as a branch and not as part of the project's name. */}
      <span className="wc-branch">
        <Icon name="git-branch" />
        <span className="wc-branch-name">{branch}</span>
      </span>
    </>
  );
}
