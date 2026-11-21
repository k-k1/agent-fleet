import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import type { Repo } from "../repos/store.ts";
import { buildRuns, type RunDelivery } from "./runs.ts";
import { ancestorNames, planClearFinished, runStopScope, runWorktreeNode } from "./runActions.ts";

const sess = (name: string, extra: Partial<Session>): Session => ({ name, kind: "claude", alive: true, ...extra });
const closed: RunDelivery = { id: "d", categoryId: "c", state: "closed", outcome: "clean", turnOutcome: "clean", sentAt: "", createdAt: "", closedAt: "", heldBy: "" };
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
    const a = [sess("r", { origin: "queue", repo: "base@wip-a" })];
    const b = [sess("r", { origin: "queue", repo: "base" })];
    expect(runWorktreeNode(buildRuns(a, new Map())[0], repos, a)!.repo.name).toBe("base@wip-a");
    expect(runWorktreeNode(buildRuns(b, new Map())[0], repos, b)).toBeNull();
  });

  it("does not offer a worktree the run did not create (the member's session ran there first)", () => {
    const sessions = [
      sess("mine", { origin: "user", repo: "base@wip-a", createdAt: "2026-11-01T00:00:00Z" }),
      sess("r", { origin: "queue", repo: "base@wip-a", createdAt: "2026-11-02T00:00:00Z" }),
    ];
    expect(runWorktreeNode(buildRuns(sessions, new Map())[0], repos, sessions)).toBeNull();
  });
});

describe("a worktree whose creator the Agent recorded", () => {
  const rec = (createdBy: string): Repo[] => [{ name: "base" }, { name: "base@wip-a", worktree: true, parent: "base", createdBy }];
  // The member's session created the copy and was archived, so the run is the first one listed.
  const sessions = [sess("r", { origin: "queue", repo: "base@wip-a", createdAt: "2026-11-02T00:00:00Z", alive: false })];

  it("is not offered when another session created it, even if the run is the first one listed", () => {
    const run = buildRuns(sessions, new Map())[0];
    expect(runWorktreeNode(run, rec("mine"), sessions)).toBeNull();
    expect(runWorktreeNode(run, rec("r"), sessions)!.repo.name).toBe("base@wip-a");
  });

  it("is not offered when a member's session sorts before the run at the same timestamp", () => {
    const tie = [
      sess("saaaaaa", { origin: "user", repo: "base@wip-a", createdAt: "2026-11-02T00:00:00Z", alive: false }),
      sess("szzzzzz", { origin: "queue", repo: "base@wip-a", createdAt: "2026-11-02T00:00:00Z", alive: false }),
    ];
    const run = buildRuns(tie, new Map())[0];
    expect(runWorktreeNode(run, rec("szzzzzz"), tie)).toBeNull();
    expect(runWorktreeNode(run, rec("szzzzzz"), [tie[1]])!.repo.name).toBe("base@wip-a");
  });

  it("is not cleared with the finished runs either", () => {
    const merged = sess("r", { origin: "queue", repo: "base@wip-a", alive: false, pr: { state: "merged" } as Session["pr"] });
    const runs = runsOf([merged], ["r"]);
    expect(planClearFinished(runs, [merged], rec("mine")).node).toBeNull();
    expect(planClearFinished(runs, [merged], rec("r")).node?.repo.name).toBe("base@wip-a");
  });
});

describe("planClearFinished", () => {
  const merged = { number: 1, state: "merged" as const, url: "u" };
  const sessions = [
    sess("r1", { origin: "queue", alive: false, repo: "base@wip-a", pr: merged, createdAt: "2026-11-01T00:00:00Z" }),
    sess("k1", { origin: "session", originSession: "r1", alive: false, repo: "base@wip-a", createdAt: "2026-11-01T01:00:00Z" }),
    sess("r2", { origin: "queue", alive: false, repo: "base@wip-b", pr: { number: 2, state: "open", url: "u" } }),
    sess("r3", { origin: "queue", alive: false, repo: "base@wip-c", pr: merged, createdAt: "2026-11-01T00:00:00Z" }),
    sess("mine", { origin: "user", alive: false, repo: "base@wip-c", createdAt: "2026-11-02T00:00:00Z" }),
    sess("r4", { origin: "queue", alive: true, repo: "base" }),
  ];
  const runs = runsOf(sessions, ["r1", "r2", "r3"]);

  it("offers the merged worktrees only, and archives the rest of the stopped finished sessions", () => {
    const p = planClearFinished(runs, sessions, repos);
    expect(p.node!.repo.name).toBe("base@wip-a");
    expect(p.node!.children).toEqual([]);
    // r1/k1 go with their worktree; r2 is unmerged; r3's copy holds a session of the member's.
    expect(p.archive.flat().map((s) => s.name).sort()).toEqual(["r2", "r3"]);
  });

  it("never touches a run that has not ended, nor a member's session", () => {
    const p = planClearFinished(runs, sessions, repos);
    expect(p.archive.flat().map((s) => s.name)).not.toContain("r4");
    expect(p.archive.flat().map((s) => s.name)).not.toContain("mine");
  });

  it("lists the run last, after what it started, and reads state from the latest list", () => {
    const base = [
      sess("r", { origin: "queue", alive: false, repo: "base" }),
      sess("k1", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-01T00:00:00Z" }),
      sess("k2", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-02T00:00:00Z" }),
    ];
    const rs = runsOf(base, ["r"]);
    expect(planClearFinished(rs, base, repos).archive[0].map((s) => s.name)).toEqual(["k1", "k2", "r"]);
    const resumed = base.map((s) => (s.name === "k1" ? { ...s, alive: true } : s));
    // k1 is alive again: the run keeps it, so the run itself is kept too.
    expect(planClearFinished(rs, resumed, repos).archive[0].map((s) => s.name)).toEqual(["k2"]);
  });

  it("keeps every ancestor of a session that cannot be archived (locked grandchild)", () => {
    const tree = [
      sess("r", { origin: "queue", alive: false, repo: "base", createdAt: "2026-11-01T00:00:00Z" }),
      sess("c", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-02T00:00:00Z" }),
      sess("g", { origin: "session", originSession: "c", alive: false, locked: true, repo: "base", createdAt: "2026-11-03T00:00:00Z" }),
      sess("c2", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-04T00:00:00Z" }),
    ];
    expect(planClearFinished(runsOf(tree, ["r"]), tree, repos).archive[0].map((s) => s.name)).toEqual(["c2"]);
  });

  it("lists a deeper session before its parent", () => {
    const tree = [
      sess("r", { origin: "queue", alive: false, repo: "base", createdAt: "2026-11-01T00:00:00Z" }),
      sess("c", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-02T00:00:00Z" }),
      sess("g", { origin: "session", originSession: "c", alive: false, repo: "base", createdAt: "2026-11-03T00:00:00Z" }),
    ];
    expect(planClearFinished(runsOf(tree, ["r"]), tree, repos).archive[0].map((s) => s.name)).toEqual(["g", "c", "r"]);
  });

  it("keeps a merged run's worktree while a session of the run lives elsewhere", () => {
    const tree = [
      sess("r", { origin: "queue", alive: false, repo: "base@wip-a", pr: { number: 1, state: "merged", url: "u" }, createdAt: "2026-11-01T00:00:00Z" }),
      sess("c", { origin: "session", originSession: "r", alive: true, repo: "base", createdAt: "2026-11-02T00:00:00Z" }),
    ];
    const rs = runsOf(tree, ["r"]);
    const p = planClearFinished(rs, tree, repos);
    expect(p.node).toBeNull();
    expect(p.archive).toEqual([]); // the root stays too: its worktree is a candidate, its child is alive
    expect(runWorktreeNode(rs[0], repos, tree)).toBeNull();
    // Once the child is gone from the list the copy is offered.
    const after = planClearFinished(rs, tree.filter((s) => s.name !== "c"), repos);
    expect(after.node!.repo.name).toBe("base@wip-a");
  });

  it("keeps a worktree two finished runs share while the other run has a session elsewhere", () => {
    const merged = { number: 1, state: "merged" as const, url: "u" };
    const tree = [
      sess("a", { origin: "queue", alive: false, repo: "base@wip-a", pr: merged, createdAt: "2026-11-01T00:00:00Z" }),
      sess("b", { origin: "queue", alive: false, repo: "base@wip-a", createdAt: "2026-11-02T00:00:00Z" }),
      sess("bc", { origin: "session", originSession: "b", alive: true, repo: "base", createdAt: "2026-11-03T00:00:00Z" }),
    ];
    const rs = runsOf(tree, ["a", "b"]);
    expect(planClearFinished(rs, tree, repos).node).toBeNull();
    expect(planClearFinished(rs, tree.filter((s) => s.name !== "bc"), repos).node!.repo.name).toBe("base@wip-a");
    // The row menu of run A refuses the same copy for the same reason.
    expect(runWorktreeNode(rs.find((r) => r.root.name === "a")!, repos, tree)).toBeNull();
    expect(runWorktreeNode(rs.find((r) => r.root.name === "a")!, repos, tree.filter((s) => s.name !== "bc"))!.repo.name).toBe("base@wip-a");
  });

  it("archives a child elsewhere first and offers the worktree once it is gone", () => {
    const tree = [
      sess("r", { origin: "queue", alive: false, repo: "base@wip-a", pr: { number: 1, state: "merged", url: "u" }, createdAt: "2026-11-01T00:00:00Z" }),
      sess("c", { origin: "session", originSession: "r", alive: false, repo: "base", createdAt: "2026-11-02T00:00:00Z" }),
    ];
    const p = planClearFinished(runsOf(tree, ["r"]), tree, repos);
    expect(p.archive.map((g) => g.map((s) => s.name))).toEqual([["c"]]);
    expect(p.node).toBeNull();
  });

  it("names the ancestors of a session, for keeping them after a failure", () => {
    const tree = [sess("r", { origin: "queue" }), sess("c", { origin: "session", originSession: "r" }), sess("g", { origin: "session", originSession: "c" })];
    expect(ancestorNames("g", tree)).toEqual(["c", "r"]);
  });

  it("chains several merged worktrees under one head", () => {
    const more = [...sessions.filter((s) => s.name !== "mine"), ];
    const p = planClearFinished(runsOf(more, ["r1", "r2", "r3"]), more, repos);
    expect([p.node!.repo.name, ...p.node!.children.map((c) => c.repo.name)].sort()).toEqual(["base@wip-a", "base@wip-c"]);
  });
});
