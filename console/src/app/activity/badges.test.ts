import { describe, expect, it } from "vitest";
import { hasUnreadConversation, hasUnreadSession, mergeAcked, pendingNews, waitingSessions, workItemNewsKeys } from "./badges.ts";
import { approvalCount } from "../../features/memo/queues.ts";
import type { Memo } from "../../types/memo.ts";
import type { FleetNotification } from "../../features/notifications/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import { failedScheduleKeys } from "../../features/schedules/read.ts";
import type { Session } from "../../types/session.ts";
import type { WorkItem } from "../../features/workitems/read.ts";
import type { ScheduleDTO } from "../../features/schedules/read.ts";

const sess = (name: string, state: string, alive = true) => ({ name, state, alive }) as unknown as Session;
const item = (key: string, updatedAt: string, commentCount: number | null = 0) =>
  ({ provider: "github", key, updatedAt, commentCount }) as unknown as WorkItem;

describe("waitingSessions", () => {
  it("counts running sessions stopped on the member's answer", () => {
    const list = [sess("a", "question"), sess("b", "plan"), sess("c", "permission"), sess("d", "working"), sess("e", "")];
    expect(waitingSessions(list)).toBe(3);
  });

  it("leaves out states nobody answers in the pane, and stopped sessions", () => {
    const list = [sess("a", "limited"), sess("b", "auth"), sess("c", "blocked"), sess("d", "question", false)];
    expect(waitingSessions(list)).toBe(0);
  });
});

describe("badges under a working set", () => {
  const set: WorkingSet = { id: "w1", name: "A", repos: ["webshop"], convs: ["c-in"], sessions: ["solo"], schedules: [] };
  const inRepo = (name: string, state: string, repo: string) => ({ name, state, alive: true, cwd: `/home/dev/repos/${repo}`, repo }) as unknown as Session;
  const note = (over: Partial<FleetNotification>): FleetNotification => ({
    seq: 1, id: "n", kind: "answer-ready", target: { type: "session", id: "s" }, displayName: "", payload: {}, createdAt: "", seen: false, ...over,
  });

  it("counts waiting sessions the set lists, by repository or by direct assignment", () => {
    const list = [inRepo("a", "question", "webshop"), inRepo("b", "question", "payments-api"), sess("solo", "plan")];
    expect(waitingSessions(list)).toBe(3);
    expect(waitingSessions(list, set)).toBe(2);
  });

  it("marks unread sessions only when one of them is listed", () => {
    const list = [inRepo("a", "", "webshop"), inRepo("b", "", "payments-api")];
    const onB = [note({ target: { type: "session", id: "b" } })];
    expect(hasUnreadSession(onB, list, null)).toBe(true);
    expect(hasUnreadSession(onB, list, set)).toBe(false);
    expect(hasUnreadSession([note({ target: { type: "session", id: "a" } })], list, set)).toBe(true);
    // A session the list does not know cannot be placed in the set.
    expect(hasUnreadSession([note({ target: { type: "session", id: "gone" } })], list, set)).toBe(false);
  });

  it("marks unread conversations only when one of them is listed", () => {
    const report = (id: string) => note({ kind: "session-report", payload: { conversation_id: id } });
    expect(hasUnreadConversation([report("c-out")], null)).toBe(true);
    expect(hasUnreadConversation([report("c-out")], set)).toBe(false);
    expect(hasUnreadConversation([report("c-in")], set)).toBe(true);
  });
});

describe("workItemNewsKeys", () => {
  const seen = { "github:o/r#1": { u: "2026-01-01T00:00:00Z", c: 0 }, "github:o/r#2": { u: "2026-01-01T00:00:00Z", c: 0 } };

  it("keys only the rows that moved past their baseline", () => {
    const keys = workItemNewsKeys([item("o/r#1", "2026-01-02T00:00:00Z"), item("o/r#2", "2026-01-01T00:00:00Z")], seen);
    expect(keys).toHaveLength(1);
    expect(keys[0]).toContain("github:o/r#1");
  });

  it("gives a row that moves again a new key, so an acknowledged row counts again", () => {
    const first = workItemNewsKeys([item("o/r#1", "2026-01-02T00:00:00Z")], seen);
    const again = workItemNewsKeys([item("o/r#1", "2026-01-03T00:00:00Z")], seen);
    expect(pendingNews(again, first)).toEqual(again);
  });
});

describe("failedScheduleKeys", () => {
  const sched = (id: string, last_status: string, last_run = "2026-01-01T00:00:00Z") => ({ id, last_status, last_run }) as unknown as ScheduleDTO;

  it("keys only schedules whose latest run failed", () => {
    expect(failedScheduleKeys([sched("a", "error: boom"), sched("b", "fired"), sched("c", "skipped_busy"), sched("d", "")])).toEqual([
      "a@2026-01-01T00:00:00Z",
    ]);
  });

  it("keys the next failure of the same schedule anew", () => {
    const first = failedScheduleKeys([sched("a", "error: boom", "2026-01-01T00:00:00Z")]);
    const next = failedScheduleKeys([sched("a", "error: boom", "2026-01-02T00:00:00Z")]);
    expect(pendingNews(next, first)).toEqual(next);
  });
});

describe("mergeAcked", () => {
  it("adds what is shown to what was acknowledged before", () => {
    expect(mergeAcked(["a"], ["b"], ["a", "b"])).toEqual(["a", "b"]);
  });

  it("keeps a key that is hidden but still exists, and drops one that no longer exists", () => {
    expect(mergeAcked(["a", "b", "old"], ["a"], ["a", "b"])).toEqual(["a", "b"]);
  });
});

describe("pendingNews", () => {
  it("is everything when nothing was acknowledged, and only the rest otherwise", () => {
    expect(pendingNews(["a", "b"], undefined)).toEqual(["a", "b"]);
    expect(pendingNews(["a", "b"], ["a"])).toEqual(["b"]);
    expect(pendingNews(["a"], ["a", "gone"])).toEqual([]);
  });
});

describe("approvalCount", () => {
  const memo = (id: string, state: string, sentAt = "") => ({ id, state, sentAt }) as unknown as Memo;

  it("counts only held notes: not plain notes, notes waiting to run, or those past that", () => {
    const list = [memo("a", "held"), memo("b", "held"), memo("c", ""), memo("d", "pending"), memo("e", "claimed"), memo("f", "delivered"), memo("g", "unconfirmed"), memo("h", "superseded")];
    expect(approvalCount(list)).toBe(2);
  });

  it("is zero for an empty list", () => {
    expect(approvalCount([])).toBe(0);
  });
});

describe("auto-run sessions are the Auto-run view's, not the Sessions view's", () => {
  const run = { name: "run", state: "question", alive: true, origin: "queue" } as unknown as Session;
  const rev = { name: "rev", state: "plan", alive: true, origin: "session", originSession: "run" } as unknown as Session;
  const own = { name: "own", state: "question", alive: true, origin: "user" } as unknown as Session;
  const note = (id: string) => ({ seq: 1, id: "n" + id, kind: "answer-ready", target: { type: "session", id }, seen: false, payload: {} }) as unknown as FleetNotification;

  it("does not count a waiting run or its reviewer", () => {
    expect(waitingSessions([run, rev])).toBe(0);
    expect(waitingSessions([run, rev, own])).toBe(1);
  });

  it("does not show an unread dot for a run's session", () => {
    expect(hasUnreadSession([note("run"), note("rev")], [run, rev, own], null)).toBe(false);
    expect(hasUnreadSession([note("run"), note("own")], [run, rev, own], null)).toBe(true);
  });
});
