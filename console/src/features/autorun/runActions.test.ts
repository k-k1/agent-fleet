import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import type { Repo } from "../repos/store.ts";
import { buildRuns, type RunDelivery } from "./runs.ts";
import { planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";

const sess = (name: string, extra: Partial<Session>): Session => ({ name, kind: "claude", alive: true, ...extra });
const closed: RunDelivery = { categoryId: "c", state: "closed", sentAt: "", createdAt: "", closedAt: "", heldBy: "" };
const repos: Repo[] = [
  { name: "base" },
  { name: "base@wip-a", worktree: true, parent: "base" },
  { name: "base@wip-b", worktree: true, parent: "base" },
  { name: "base@wip-c", worktree: true, parent: "base" },
];
const runsOf = (sessions: Session[], names: string[]) => buildRuns(sessions, new Map(names.map((n) => [n, closed])));

describe("runStopScope", () => {
  const sessions = [
    sess("run", { origin: "queue", repo: "base@wip-a" }),
    sess("rev", { origin: "session", originSession: "run", repo: "base@wip-b" }),
    sess("mine", { origin: "user", repo: "base@wip-a" }),
  ];
  const [run] = buildRuns(sessions, new Map());

  it("names only the run's live sessions, however many copies they use", () => {
    const s = runStopScope(run, repos)!;
    expect([...s.only].sort()).toEqual(["rev", "run"]);
    expect(s.node.repo.name).toBe("base@wip-a");
    expect(s.node.children.map((c) => c.repo.name)).toEqual(["base@wip-b"]);
  });

  it("is null once nothing of the run is alive", () => {
    const [stopped] = buildRuns(sessions.map((x) => ({ ...x, alive: false })), new Map());
    expect(runStopScope(stopped, repos)).toBeNull();
  });
});

describe("runWorktreeNode", () => {
  it("offers a worktree and never a base clone", () => {
    const [wt] = buildRuns([sess("r", { origin: "queue", repo: "base@wip-a" })], new Map());
    const [bc] = buildRuns([sess("r", { origin: "queue", repo: "base" })], new Map());
    expect(runWorktreeNode(wt, repos)!.repo.name).toBe("base@wip-a");
    expect(runWorktreeNode(bc, repos)).toBeNull();
  });
});

describe("planClearFinished", () => {
  const merged = { number: 1, state: "merged" as const, url: "u" };
  const sessions = [
    sess("r1", { origin: "queue", alive: false, repo: "base@wip-a", pr: merged }),
    sess("k1", { origin: "session", originSession: "r1", alive: false, repo: "base@wip-a" }),
    sess("r2", { origin: "queue", alive: false, repo: "base@wip-b", pr: { number: 2, state: "open", url: "u" } }),
    sess("r3", { origin: "queue", alive: false, repo: "base@wip-c", pr: merged }),
    sess("mine", { origin: "user", alive: false, repo: "base@wip-c" }),
    sess("r4", { origin: "queue", alive: true, repo: "base" }),
  ];
  const runs = runsOf(sessions, ["r1", "r2", "r3"]);

  it("offers the merged worktrees only, and archives the rest of the stopped finished sessions", () => {
    const p = planClearFinished(runs, sessions, repos);
    expect(p.node!.repo.name).toBe("base@wip-a");
    expect(p.node!.children).toEqual([]);
    // r1/k1 go with their worktree; r2 is unmerged; r3's copy holds a session of the member's.
    expect(p.archive.map((s) => s.name).sort()).toEqual(["r2", "r3"]);
  });

  it("never touches a run that has not ended, nor a member's session", () => {
    const p = planClearFinished(runs, sessions, repos);
    expect(p.archive.map((s) => s.name)).not.toContain("r4");
    expect(p.archive.map((s) => s.name)).not.toContain("mine");
  });

  it("chains several merged worktrees under one head", () => {
    const more = [...sessions.filter((s) => s.name !== "mine"), ];
    const p = planClearFinished(runsOf(more, ["r1", "r2", "r3"]), more, repos);
    expect([p.node!.repo.name, ...p.node!.children.map((c) => c.repo.name)].sort()).toEqual(["base@wip-a", "base@wip-c"]);
  });
});
