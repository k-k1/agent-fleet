import { describe, it, expect } from "vitest";
import type { Repo } from "../features/repos/store.ts";
import type { Session } from "../types/session.ts";
import { sessionFolder, orderedRepos, groupedRepos, sessionsInFolder, orphanSessions } from "./project.ts";

const repo = (name: string, extra: Partial<Repo> = {}): Repo => ({ name, ...extra });
const wt = (name: string, parent: string, extra: Partial<Repo> = {}): Repo => ({
  name,
  worktree: true,
  parent,
  ...extra,
});
const sess = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "claude", ...extra });

describe("sessionFolder", () => {
  it("uses repo when present", () => {
    expect(sessionFolder(sess("s1", { repo: "agent-fleet" }))).toBe("agent-fleet");
  });
  it("falls back to the dir basename", () => {
    expect(sessionFolder(sess("s1", { dir: "/home/dev/repos/foo" }))).toBe("foo");
  });
  it("is empty for a repo-less session", () => {
    expect(sessionFolder(sess("s1"))).toBe("");
    expect(sessionFolder(sess("s1", { dir: "" }))).toBe("");
  });
});

describe("orderedRepos", () => {
  it("groups each base with its worktrees, then the next base", () => {
    const repos = [
      wt("af@wip-b", "agent-fleet"),
      repo("zzz"),
      repo("agent-fleet"),
      wt("af@wip-a", "agent-fleet"),
    ];
    expect(orderedRepos(repos).map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-a",
      "af@wip-b",
      "zzz",
    ]);
  });
  it("keeps a worktree whose base is gone as a trailing node", () => {
    const repos = [repo("keep"), wt("orphan@wt", "deleted-base")];
    expect(orderedRepos(repos).map((r) => r.name)).toEqual(["keep", "orphan@wt"]);
  });
  it("returns [] for no repos", () => {
    expect(orderedRepos([])).toEqual([]);
  });
});

describe("groupedRepos", () => {
  it("clusters each base with its worktrees; orphan worktree is its own group", () => {
    const repos = [
      wt("af@wip-b", "agent-fleet"),
      repo("zzz"),
      repo("agent-fleet"),
      wt("af@wip-a", "agent-fleet"),
      wt("orphan@wt", "deleted-base"),
    ];
    expect(groupedRepos(repos).map((g) => g.map((r) => r.name))).toEqual([
      ["agent-fleet", "af@wip-a", "af@wip-b"],
      ["zzz"],
      ["orphan@wt"],
    ]);
  });

  it("orders a base's worktrees by createdAt (oldest first), ignoring slug order", () => {
    const repos = [
      repo("agent-fleet"),
      // Slug order (name) would be b, c, z; creation order is z (oldest) → b → c.
      wt("af@wip-c", "agent-fleet", { createdAt: "2026-07-15T05:00:00Z" }),
      wt("af@wip-z", "agent-fleet", { createdAt: "2026-07-15T03:00:00Z" }),
      wt("af@wip-b", "agent-fleet", { createdAt: "2026-07-15T04:00:00Z" }),
    ];
    expect(groupedRepos(repos)[0].map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-z",
      "af@wip-b",
      "af@wip-c",
    ]);
  });

  it("falls back to name when a worktree has no createdAt", () => {
    const repos = [
      repo("agent-fleet"),
      wt("af@wip-b", "agent-fleet"),
      wt("af@wip-a", "agent-fleet", { createdAt: "2026-07-15T09:00:00Z" }),
    ];
    // The timestamped one sorts among the untimed by name (deterministic).
    expect(groupedRepos(repos)[0].map((r) => r.name)).toEqual([
      "agent-fleet",
      "af@wip-a",
      "af@wip-b",
    ]);
  });
});

describe("sessionsInFolder", () => {
  it("filters to the folder, newest first", () => {
    const sessions = [
      sess("a", { repo: "agent-fleet", createdAt: "2026-01-01" }),
      sess("b", { repo: "other" }),
      sess("c", { repo: "agent-fleet", createdAt: "2026-02-01" }),
    ];
    expect(sessionsInFolder(sessions, "agent-fleet").map((s) => s.name)).toEqual(["c", "a"]);
  });
});

describe("orphanSessions", () => {
  it("returns sessions whose folder is not a known repo", () => {
    const repos = [repo("agent-fleet")];
    const sessions = [
      sess("in", { repo: "agent-fleet" }),
      sess("shell"), // no repo/dir
      sess("gone", { repo: "removed-repo" }),
    ];
    expect(orphanSessions(sessions, repos).map((s) => s.name).sort()).toEqual(["gone", "shell"]);
  });
});
