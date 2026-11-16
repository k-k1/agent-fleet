import { describe, expect, it } from "vitest";
import { pendingNews, waitingSessions, workItemNewsKeys } from "./badges.ts";
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

describe("pendingNews", () => {
  it("is everything when nothing was acknowledged, and only the rest otherwise", () => {
    expect(pendingNews(["a", "b"], undefined)).toEqual(["a", "b"]);
    expect(pendingNews(["a", "b"], ["a"])).toEqual(["b"]);
    expect(pendingNews(["a"], ["a", "gone"])).toEqual([]);
  });
});
