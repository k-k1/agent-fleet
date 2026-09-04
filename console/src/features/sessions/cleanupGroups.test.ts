import { describe, it, expect } from "vitest";
import { groupCandidates, rowLabel, type CleanupCandidate } from "./cleanupGroups.ts";

const wt = (repo: string, branch: string, safety: CleanupCandidate["safety"] = "safe"): CleanupCandidate => ({
  type: "worktree",
  action: safety === "keep" ? undefined : "delete_worktree",
  id: repo,
  repo,
  branch,
  safety,
  reason: "r",
});
const sess = (name: string, repo: string, safety: CleanupCandidate["safety"] = "review"): CleanupCandidate => ({
  type: "session",
  action: safety === "keep" ? undefined : "archive_session",
  id: name,
  display: name,
  repo,
  safety,
  reason: "r",
});
const br = (repo: string, branch: string): CleanupCandidate => ({
  type: "branch",
  action: "delete_branch",
  id: repo,
  repo,
  branch,
  safety: "safe",
  reason: "r",
});

describe("groupCandidates", () => {
  it("nests working copies under their base repo, worktrees before the clone", () => {
    const repos = groupCandidates([
      br("agent-fleet", "temp/old"),
      wt("agent-fleet@wip-b", "temp/b"),
      wt("novel-idea@wip-z", "temp/z"),
      wt("agent-fleet@wip-a", "temp/a"),
    ]);
    expect(repos.map((r) => r.repo)).toEqual(["agent-fleet", "novel-idea"]);
    expect(repos[0].copies.map((c) => c.key)).toEqual([
      "agent-fleet@wip-a",
      "agent-fleet@wip-b",
      "agent-fleet", // the clone itself sorts last
    ]);
    expect(repos[0].count).toBe(3);
    expect(repos[0].copies[0]).toMatchObject({ isWorktree: true, suffix: "@wip-a", branch: "temp/a" });
    expect(repos[0].copies[2]).toMatchObject({ isWorktree: false, suffix: "", repo: "agent-fleet" });
  });

  // A session goes under the worktree it belongs to, so that one group shows everything
  // deleting that working copy takes with it (delete_worktree prunes the sessions inside).
  it("puts a session in the working copy it runs in", () => {
    const repos = groupCandidates([sess("s1", "agent-fleet@wip-a"), wt("agent-fleet@wip-a", "temp/a")]);
    const copy = repos[0].copies[0];
    expect(copy.key).toBe("agent-fleet@wip-a");
    expect(copy.rows.map((r) => r.type)).toEqual(["worktree", "session"]); // coarse → fine
  });

  it("counts only actionable safe rows (keep rows are shown but never selected)", () => {
    const repos = groupCandidates([
      wt("agent-fleet@wip-a", "temp/a", "keep"),
      wt("agent-fleet@wip-b", "temp/b", "safe"),
      br("agent-fleet", "temp/old"),
    ]);
    expect(repos[0].safeCount).toBe(2);
    expect(repos[0].copies.find((c) => c.key === "agent-fleet@wip-a")?.safeCount).toBe(0);
  });

  // An orphan pane with no metadata has no known home (repo is empty). Rather than dropping
  // it, list it last in a catch-all group.
  it("keeps rows with no known working copy in a trailing catch-all group", () => {
    const orphan: CleanupCandidate = { type: "session", id: "ghost", safety: "review", reason: "r" };
    const repos = groupCandidates([orphan, wt("agent-fleet@wip-a", "temp/a")]);
    expect(repos.map((r) => r.repo)).toEqual(["agent-fleet", ""]);
    expect(repos[1].copies[0].rows).toHaveLength(1);
  });
});

describe("rowLabel", () => {
  it("drops the worktree's own name (its group heading already carries it)", () => {
    expect(rowLabel(wt("agent-fleet@wip-a", "temp/a"))).toBe("");
    expect(rowLabel(br("agent-fleet", "temp/old"))).toBe("temp/old");
    expect(rowLabel(sess("左ペイン更新の遅延", "agent-fleet@wip-a"))).toBe("左ペイン更新の遅延");
  });
});
