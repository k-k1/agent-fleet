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

  // #5219: a session started through a link spells another path and stores no repository place;
  // the working-copy id the Agent reads from the directory now leads back to the copy, even when
  // the link was pointed elsewhere since the session started.
  it("matches a session started through a link by its current working-copy id, when one row has it", () => {
    const a: Repo = { ...WT, workingCopyId: "wc_a" };
    const b: Repo = { name: "app@wip-y", path: "/home/dev/repos/app@wip-y", worktree: true, parent: "app", branch: "temp/y", workingCopyId: "wc_b" };
    const linked = sess({ repo: "current", dir: "/home/dev/links/current", worktree: true, place: { kind: "other" }, workingCopyId: "wc_b", currentBranch: "temp/y" });
    expect(handoffLaunchTarget(linked, [GIT, a, b], true)).toEqual({ repo: { ...GIT, branch: "temp/y" } });
    expect(handoffLaunchTarget(linked, [GIT, a, b], false)).toMatchObject({ repo: { name: "app@wip-y", path: "/home/dev/links/current" } });
    // two rows with one id (a file copy of a clone) say nothing about which one it is
    const twin: Repo = { ...b, name: "app@twin", path: "/home/dev/repos/app@twin" };
    expect(handoffLaunchTarget(linked, [GIT, b, twin], true)).toEqual({ error: "no_parent" });
  });

  it("reports no_dir for a session with no working directory", () => {
    expect(handoffLaunchTarget(sess({}), [GIT], false)).toEqual({ error: "no_dir" });
  });

  it("falls back to the session's own fields when the rail has no row for it", () => {
    const t = handoffLaunchTarget(sess({ repo: "ghost", dir: "/home/dev/repos/ghost", branch: "main" }), [], false);
    expect(t).toEqual({ repo: { name: "ghost", path: "/home/dev/repos/ghost", branch: "main", worktree: undefined } });
  });

  // A space is never a row of the repos rail. Built from the session alone it would pass for a
  // git working copy (no vcs) and the launch dialog would offer a worktree and a branch in it.
  describe("a session running in a space", () => {
    const inSpace = (o: Partial<Session> = {}) => sess({ space: "research-aaa", dir: "/home/dev/spaces/research-aaa", ...o });

    it("targets the space by its folder and carries nothing a repository dialog could read", () => {
      const t = handoffLaunchTarget(inSpace(), [GIT], false);
      expect(t).toEqual({ space: "research-aaa", dir: "/home/dev/spaces/research-aaa" });
      expect("repo" in t).toBe(false);
    });

    it("stays on the space even when a repository row happens to share its folder name or path", () => {
      const clash: Repo = { name: "research-aaa", path: "/home/dev/spaces/research-aaa", branch: "main" };
      expect(handoffLaunchTarget(inSpace({ repo: "research-aaa" }), [clash], false)).toMatchObject({ space: "research-aaa" });
    });

    it("ignores the new-worktree opt-in: a space has no parent clone to branch from", () => {
      expect(handoffLaunchTarget(inSpace({ worktree: true }), [GIT, WT], true)).toMatchObject({ space: "research-aaa" });
    });

    it("takes the space from the stored place on a row that carries no space (#5219)", () => {
      const clash: Repo = { name: "research-aaa", path: "/home/dev/repos/research-aaa", branch: "main" };
      const stopped = sess({ repo: "research-aaa", place: { kind: "space", id: "research-aaa" }, dir: "/home/dev/spaces/research-aaa" });
      expect(handoffLaunchTarget(stopped, [clash], false)).toEqual({ space: "research-aaa", dir: "/home/dev/spaces/research-aaa" });
    });

    it("needs no directory to name the space", () => {
      expect(handoffLaunchTarget(inSpace({ dir: undefined }), [], false)).toEqual({ space: "research-aaa", dir: "" });
    });
  });
});
