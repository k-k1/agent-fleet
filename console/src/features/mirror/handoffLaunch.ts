// Which working copy a handoff proposal's "launch" hands to the launch dialog.
//
// The dialog decides what it may offer from the Repo it is given — a new worktree, a
// branch name, a base point — so a target assembled from the SESSION alone is a target
// missing everything the session does not carry. A Session has no `vcs` and no `unborn`
// flag, and an absent `vcs` reads as git: an SVN checkout (docs/log/41) was therefore
// offered "new working copy (worktree)" plus branch fields, none of which exist in SVN.
// That is why the working copy is looked up in the repos rail here and carried WHOLE:
// every future Repo flag reaches the dialog without another site to remember.
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

/** Resolved target, or why one could not be built (the caller turns it into a toast). */
export type HandoffTarget = { repo: Repo } | { error: "no_dir" | "no_parent" };

/** The repos rail's row for this session's working copy. Matched on `dir`/`path` first —
 *  the only reliable link between a session and its copy — with the folder name as the
 *  fallback for a row the list has not caught up with. */
function repoOf(session: Session | null | undefined, repos: Repo[]): Repo | undefined {
  const dir = session?.dir || session?.path || "";
  return repos.find((r) => !!r.path && r.path === dir) || repos.find((r) => r.name === session?.repo);
}

/** Build the launch target for a handoff proposal.
 *
 *  newWorktree is the card's own opt-in, offered only for a session already running in a
 *  worktree: the launch then targets the PARENT clone with a new worktree branched off
 *  this session's current branch, instead of sharing the source's directory. */
export function handoffLaunchTarget(
  session: Session | null | undefined,
  repos: Repo[],
  newWorktree: boolean,
): HandoffTarget {
  const path = session?.dir || session?.path || "";
  if (!path) return { error: "no_dir" };
  const mine = repoOf(session, repos);
  const branch = session?.currentBranch || session?.branch;
  if (newWorktree && session?.worktree) {
    const base = mine?.parent ? repos.find((r) => r.name === mine.parent) : undefined;
    if (!base?.path) return { error: "no_parent" };
    return { repo: { ...base, branch } };
  }
  // No row yet (a session in a folder the rail has not listed): fall back to what the
  // session itself knows. `vcs` is then unknown, exactly as before — but that path no
  // longer covers the ordinary case, which is what made the SVN bug permanent.
  if (!mine) {
    return {
      repo: {
        name: session?.repo || path.split("/").filter(Boolean).at(-1) || session?.name || "",
        path,
        branch,
        worktree: session?.worktree,
      },
    };
  }
  return { repo: { ...mine, path, branch, worktree: session?.worktree ?? mine.worktree } };
}
