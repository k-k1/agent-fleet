// The bug this pins shut: launching a handoff proposal from a session running in an SVN
// checkout offered the git-only location choice (new worktree / branch name / base
// branch). The target was assembled from the Session, which has no `vcs`, and an absent
// `vcs` reads as git everywhere downstream (StartHost's allowWorktree).
import { describe, it, expect } from "vitest";
import { handoffLaunchTarget } from "./handoffLaunch.ts";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

const sess = (o: Partial<Session>): Session => ({ name: "s1", kind: "claude", ...o }) as Session;

const SVN: Repo = { name: "docs", path: "/home/dev/repos/docs", vcs: "svn", revision: "42", url: "https://svn.example.com/docs/trunk" };
const GIT: Repo = { name: "app", path: "/home/dev/repos/app", branch: "develop" };
const WT: Repo = { name: "app@wip-x", path: "/home/dev/repos/app@wip-x", worktree: true, parent: "app", branch: "temp/x" };

describe("handoffLaunchTarget", () => {
  it("carries vcs for an SVN working copy — the whole point", () => {
    const t = handoffLaunchTarget(sess({ repo: "docs", dir: SVN.path }), [SVN, GIT], false);
    expect(t).toEqual({ repo: { ...SVN, branch: undefined, worktree: undefined } });
    expect("repo" in t && t.repo.vcs).toBe("svn");
  });

  it("carries unborn too (a repo with no commit cannot host a worktree either)", () => {
    const unborn: Repo = { name: "fresh", path: "/home/dev/repos/fresh", branch: "main", unborn: true };
    const t = handoffLaunchTarget(sess({ repo: "fresh", dir: unborn.path }), [unborn], false);
    expect("repo" in t && t.repo.unborn).toBe(true);
  });

  it("matches on the directory, not the folder name (two rows can share a name only by dir)", () => {
    const t = handoffLaunchTarget(sess({ repo: "wrong", dir: SVN.path }), [GIT, SVN], false);
    expect("repo" in t && t.repo.name).toBe("docs");
  });

  it("prefers the session's live branch over the row's", () => {
    const t = handoffLaunchTarget(sess({ repo: "app", dir: GIT.path, branch: "develop", currentBranch: "feature/z" }), [GIT], false);
    expect("repo" in t && t.repo.branch).toBe("feature/z");
  });

  it("new-worktree opt-in targets the PARENT clone, branched off this worktree's branch", () => {
    const t = handoffLaunchTarget(sess({ repo: "app@wip-x", dir: WT.path, worktree: true, branch: "temp/x" }), [GIT, WT], true);
    expect(t).toEqual({ repo: { ...GIT, branch: "temp/x" } });
  });

  it("reports no_parent when the base clone is not in the list", () => {
    const t = handoffLaunchTarget(sess({ repo: "app@wip-x", dir: WT.path, worktree: true }), [WT], true);
    expect(t).toEqual({ error: "no_parent" });
  });

  it("reports no_dir for a session with no working directory", () => {
    expect(handoffLaunchTarget(sess({}), [GIT], false)).toEqual({ error: "no_dir" });
  });

  it("falls back to the session's own fields when the rail has no row for it", () => {
    const t = handoffLaunchTarget(sess({ repo: "ghost", dir: "/home/dev/repos/ghost", branch: "main" }), [], false);
    expect(t).toEqual({ repo: { name: "ghost", path: "/home/dev/repos/ghost", branch: "main", worktree: undefined } });
  });
});
