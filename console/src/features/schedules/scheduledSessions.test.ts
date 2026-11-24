import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import { buildScheduledRuns, groupRunsBySchedule, waitingScheduledCount } from "./scheduledSessions.ts";

const sess = (name: string, extra: Partial<Session>): Session => ({ name, kind: "claude", alive: true, ...extra });

describe("buildScheduledRuns", () => {
  const sessions = [
    sess("old", { origin: "schedule", alive: false, createdAt: "2026-11-01T00:00:00Z" }),
    sess("cron", { origin: "schedule", createdAt: "2026-11-02T00:00:00Z" }),
    sess("rev", { origin: "session", originSession: "cron", createdAt: "2026-11-02T01:00:00Z", state: "question" }),
    // A handoff the member launched keeps origin=user: it is theirs, not the schedule's.
    sess("hf", { origin: "user", originSession: "cron" }),
    sess("run", { origin: "queue" }),
  ];

  it("lists the schedule's runs newest first with the sessions they spawned", () => {
    const rows = buildScheduledRuns(sessions);
    expect(rows.map((r) => r.root.name)).toEqual(["cron", "old"]);
    expect(rows[0].children.map((c) => c.name)).toEqual(["rev"]);
    expect(rows.map((r) => r.live)).toEqual([true, false]);
    expect(rows[0].waiting).toBe(true);
  });

  it("counts the live sessions that wait for an answer", () => {
    expect(waitingScheduledCount(sessions)).toBe(1);
    expect(waitingScheduledCount([sess("c", { origin: "schedule", state: "working" })])).toBe(0);
  });

  it("shows only the children the set shows, and an anchor root's own state does not keep the run live", () => {
    const repos = [{ name: "a" }, { name: "b" }] as never;
    const ss = [
      sess("cron", { origin: "schedule", repo: "a", state: "question" }), // outside the set, waiting
      sess("rev", { origin: "session", originSession: "cron", repo: "b", alive: false }), // in the set, ended
      sess("rev2", { origin: "session", originSession: "cron", repo: "a", state: "question" }), // outside the set
    ];
    const setB = { id: "w", name: "w", repos: ["b"], sessions: [], convs: [], schedules: [] } as never;
    const setA = { id: "w", name: "w", repos: ["a"], sessions: [], convs: [], schedules: [] } as never;
    const b = buildScheduledRuns(ss, setB, repos);
    expect(b[0].children.map((c) => c.name)).toEqual(["rev"]);
    expect(b[0].live).toBe(false);
    expect(b[0].rootShown).toBe(false);
    expect(waitingScheduledCount(ss, setB, repos)).toBe(0);
    const a = buildScheduledRuns(ss, setA, repos);
    expect(a[0].children.map((c) => c.name)).toEqual(["rev2"]);
    expect(a[0].live).toBe(true);
    expect(a[0].rootShown).toBe(true);
    expect(waitingScheduledCount(ss, setA, repos)).toBe(2);
  });

  it("counts a stopped session that still carries its question", () => {
    expect(waitingScheduledCount([sess("c", { origin: "schedule", alive: false, carried: "question" })])).toBe(1);
  });

  it("narrows to a working set by the run's root session", () => {
    const set = { id: "w", name: "w", repos: [], sessions: ["cron", "rev"], convs: [], schedules: [] } as never;
    const rows = buildScheduledRuns([...sessions, sess("other", { origin: "schedule", state: "question" })], set);
    expect(rows.map((r) => r.root.name)).toEqual(["cron"]);
    expect(waitingScheduledCount([...sessions, sess("other", { origin: "schedule", state: "question" })], set)).toBe(1);
  });

  it("places a session by its folder's base, also for a worktree with a custom folder name", () => {
    const repos = [{ name: "app" }, { name: "nightly", worktree: true, parent: "app" }] as never;
    const set = { id: "w", name: "w", repos: ["app"], sessions: [], convs: [], schedules: [] } as never;
    const ss = [sess("cron", { origin: "schedule", repo: "nightly", state: "question" })];
    expect(buildScheduledRuns(ss, set, repos).map((r) => r.root.name)).toEqual(["cron"]);
    expect(waitingScheduledCount(ss, set, repos)).toBe(1);
  });

  it("shows a run whose child is in the set, and counts only the sessions that are", () => {
    const set = { id: "w", name: "w", repos: ["b"], sessions: [], convs: [], schedules: [] } as never;
    const ss = [
      sess("cron", { origin: "schedule", repo: "a", state: "question" }),
      sess("rev", { origin: "session", originSession: "cron", repo: "b", state: "question" }),
    ];
    const rows = buildScheduledRuns(ss, set, [{ name: "a" }, { name: "b" }] as never);
    expect(rows.map((r) => r.root.name)).toEqual(["cron"]); // the root anchors the child
    expect(rows[0].children.map((c) => c.name)).toEqual(["rev"]);
    expect(rows[0].waiting).toBe(true); // from the child in the set, not the root outside it
    expect(waitingScheduledCount(ss, set, [{ name: "a" }, { name: "b" }] as never)).toBe(1);
  });

  it("answers an empty list for a cycle or a vanished parent", () => {
    expect(buildScheduledRuns([sess("x", { origin: "session", originSession: "y" }), sess("y", { origin: "session", originSession: "x" })])).toEqual([]);
  });
});

describe("groupRunsBySchedule", () => {
  it("files runs under a listed schedule by id and leaves unknown or id-less ones in the rest", () => {
    const runs = buildScheduledRuns([
      sess("a2", { origin: "schedule", scheduleId: "A", createdAt: "2026-11-02T00:00:00Z" }),
      sess("a1", { origin: "schedule", scheduleId: "A", createdAt: "2026-11-01T00:00:00Z" }),
      sess("b", { origin: "schedule", scheduleId: "B", createdAt: "2026-11-03T00:00:00Z" }),
      sess("none", { origin: "schedule", createdAt: "2026-11-04T00:00:00Z" }),
    ]);
    const g = groupRunsBySchedule(runs, new Set(["A"]));
    expect(g.bySchedule.get("A")?.map((r) => r.root.name)).toEqual(["a2", "a1"]);
    expect(g.rest.map((r) => r.root.name)).toEqual(["none", "b"]);
  });
});

describe("waitingScheduledCount with a schedule shown in the Sessions view", () => {
  const ss = [
    sess("cron", { origin: "schedule", scheduleId: "s1", state: "question" }),
    sess("kid", { origin: "session", originSession: "cron", worktree: true, state: "question" }),
    sess("plain", { origin: "session", originSession: "cron", state: "question" }),
  ];
  it("counts a worktree child on the Sessions icon only, and keeps every run listed", () => {
    expect(waitingScheduledCount(ss)).toBe(3);
    expect(waitingScheduledCount(ss, null, [], new Set(["s1"]))).toBe(2);
    expect(buildScheduledRuns(ss)[0].children.map((c) => c.name)).toEqual(["kid", "plain"]);
  });
  it("keeps a stopped child that still carries its question on the Schedules icon: the Sessions icon counts only live ones", () => {
    const stopped = [
      sess("cron", { origin: "schedule", scheduleId: "s1", state: "working" }),
      sess("kid", { origin: "session", originSession: "cron", worktree: true, alive: false, carried: "question" }),
    ];
    expect(waitingScheduledCount(stopped, null, [], new Set(["s1"]))).toBe(1);
  });
  it("keeps a child on the Schedules icon when the working set places it differently on the Sessions side", () => {
    // Sessions' badge places by folder name (sessionInSet); the set holds the base repo only, so the
    // custom-named worktree is not counted there and must stay counted here.
    const set = { id: "w", name: "w", repos: ["app"], convs: [], sessions: [] } as never;
    const repos = [{ name: "app" }, { name: "review", worktree: true, parent: "app" }] as never;
    const custom = [
      sess("cron", { origin: "schedule", scheduleId: "s1", repo: "review", state: "working" }),
      sess("kid", { origin: "session", originSession: "cron", worktree: true, repo: "review", state: "question" }),
    ];
    expect(waitingScheduledCount(custom, set, repos, new Set(["s1"]))).toBe(1);
  });
});
