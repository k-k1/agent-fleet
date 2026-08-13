import { describe, expect, it } from "vitest";
import { groupedSharedSessions } from "./sharedProject.ts";
import type { SharedSession } from "./store.ts";

const session = (over: Partial<SharedSession> & { id: string }): SharedSession => ({
  ownerUserKey: "owner",
  name: over.id,
  kind: "codex",
  state: "stopped",
  permission: "ro",
  workspaceState: "running",
  ...over,
});

describe("groupedSharedSessions", () => {
  it("pairs worktrees with their base copy", () => {
    const groups = groupedSharedSessions([
      session({ id: "a", repo: "proj", workingCopyId: "wc-base" }),
      session({ id: "b", repo: "proj@feat", workingCopyId: "wc-wt", worktree: true, parent: "proj" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].projectName).toBe("proj");
    expect(groups[0].copies.map((c) => c.repo)).toEqual(["proj", "proj@feat"]);
  });

  // repo 共有はプロジェクト全体に効くが、ベース直下にセッションが1つも無い運用
  // (セッションごとに worktree を切る)は普通にある。その場合でも見出しは1つ。
  it("keeps worktrees of one project together when the base has no shared session", () => {
    const groups = groupedSharedSessions([
      session({ id: "a", repo: "proj@one", workingCopyId: "wc-1", worktree: true, parent: "proj", createdAt: "2026-01-01" }),
      session({ id: "b", repo: "proj@two", workingCopyId: "wc-2", worktree: true, parent: "proj", createdAt: "2026-01-02" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].projectName).toBe("proj");
    expect(groups[0].copies.map((c) => c.repo)).toEqual(["proj@one", "proj@two"]);
  });

  it("groups per owner even when two owners share the same project name", () => {
    const groups = groupedSharedSessions([
      session({ id: "a", ownerUserKey: "alice", repo: "proj", workingCopyId: "wc-a" }),
      session({ id: "b", ownerUserKey: "bob", repo: "proj", workingCopyId: "wc-b" }),
    ]);
    expect(groups.map((g) => g.ownerUserKey)).toEqual(["alice", "bob"]);
  });
});
