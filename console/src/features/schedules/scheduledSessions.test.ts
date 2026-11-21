import { describe, expect, it } from "vitest";
import type { Session } from "../../types/session.ts";
import { buildScheduledRuns, waitingScheduledCount } from "./scheduledSessions.ts";

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

  it("counts a stopped session that still carries its question", () => {
    expect(waitingScheduledCount([sess("c", { origin: "schedule", alive: false, carried: "question" })])).toBe(1);
  });

  it("narrows to a working set by the run's root session", () => {
    const set = { id: "w", name: "w", repos: [], sessions: ["cron"], convs: [], schedules: [] } as never;
    const rows = buildScheduledRuns([...sessions, sess("other", { origin: "schedule", state: "question" })], set);
    expect(rows.map((r) => r.root.name)).toEqual(["cron"]);
    expect(waitingScheduledCount([...sessions, sess("other", { origin: "schedule", state: "question" })], set)).toBe(1);
  });

  it("answers an empty list for a cycle or a vanished parent", () => {
    expect(buildScheduledRuns([sess("x", { origin: "session", originSession: "y" }), sess("y", { origin: "session", originSession: "x" })])).toEqual([]);
  });
});
