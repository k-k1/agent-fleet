// Where "start a studio" runs the agent. A worktree is never cut from a worktree's own folder
// (the Agent would name it repo@a@b under the wrong node), and a place that cannot host the agent
// says why instead of starting it somewhere else.
import { describe, expect, it } from "vitest";
import { orderedPlaces, planPlace } from "./attachPlan.ts";
import type { Repo } from "../repos/store.ts";

const BASE: Repo = { name: "app", path: "/r/app", branch: "main" };
const WT: Repo = { name: "app@x", path: "/r/app@x", branch: "temp/x", worktree: true, parent: "app" };
const SVN: Repo = { name: "legacy", path: "/r/legacy", vcs: "svn" };
const repos = [WT, BASE, SVN];

describe("planPlace", () => {
  it("runs in a worktree's own folder when the agent allows it", () => {
    expect(planPlace({ repo: WT, repos, kind: "claude", driver: "tui", want: false })).toMatchObject({
      dir: "/r/app@x",
      worktree: false,
      worktreeChoice: true,
    });
  });
  it("cuts from the parent at the worktree's branch when a worktree is required", () => {
    expect(planPlace({ repo: WT, repos, kind: "codex", driver: "managed", want: false })).toMatchObject({
      dir: "/r/app",
      worktree: true,
      base: "temp/x",
      worktreeChoice: false,
      forcedFromWorktree: true,
    });
  });
  it("a base clone takes the picked base, else its own branch", () => {
    expect(planPlace({ repo: BASE, repos, kind: "claude", driver: "tui", want: true })).toMatchObject({ worktree: true, base: "main" });
    expect(planPlace({ repo: BASE, repos, kind: "claude", driver: "tui", want: true, base: "dev" }).base).toBe("dev");
    expect(planPlace({ repo: BASE, repos, kind: "claude", driver: "tui", want: false })).toMatchObject({ worktree: false, base: "" });
  });
  it("blocks what cannot run: home or svn with an agent that needs a worktree", () => {
    expect(planPlace({ repo: null, repos, kind: "codex", driver: "managed", want: true }).blocked).toBe("needs_repo");
    expect(planPlace({ repo: SVN, repos, kind: "codex", driver: "managed", want: true }).blocked).toBe("no_worktree");
    expect(planPlace({ repo: SVN, repos, kind: "codex", driver: "tui", want: true })).toMatchObject({ dir: "/r/legacy", worktree: false });
  });
});

describe("orderedPlaces", () => {
  it("lists each worktree under its parent", () => {
    expect(orderedPlaces(repos).map((p) => `${p.nested ? "  " : ""}${p.repo.name}`)).toEqual(["app", "  app@x", "legacy"]);
  });
  it("keeps a worktree whose parent is not listed", () => {
    expect(orderedPlaces([WT]).map((p) => p.repo.name)).toEqual(["app@x"]);
  });
});
