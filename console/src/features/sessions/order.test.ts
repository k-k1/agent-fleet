// Ordering of the command palette's session list. The promise — most recently waiting for
// input at the top, stopped at the bottom — is pinned here because this is its only
// implementation.
import { describe, expect, it } from "vitest";
import { sessionTier, sortSessionsByAttention, waitingAtFromNotifications, TIER_ALIVE, TIER_STOPPED, TIER_WAITING } from "./order.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, extra: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  alive: true,
  createdAt: "2026-09-01T00:00:00Z",
  ...extra,
});

/** name → the last time it started waiting for a person; 0 (unknown) when absent. */
const ledger = (map: Record<string, number>) => (name: string) => map[name] || 0;
const names = (list: Session[]) => list.map((x) => x.name);

describe("sessionTier", () => {
  it("treats only a live question/plan/permission as waiting for input", () => {
    expect(sessionTier(s("a", { state: "question" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("b", { state: "plan" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("c", { state: "permission" }))).toBe(TIER_WAITING);
    expect(sessionTier(s("d", { state: "working" }))).toBe(TIER_ALIVE);
    // Waiting for a usage-limit reset is waiting on a clock: there is nothing for a person
    // to answer, so it does not reach the top stage.
    expect(sessionTier(s("e", { state: "limited" }))).toBe(TIER_ALIVE);
    // A row folded while holding a question is still in the stopped stage.
    expect(sessionTier(s("f", { alive: false, carried: "question" }))).toBe(TIER_STOPPED);
  });
});

describe("sortSessionsByAttention", () => {
  it("puts the most recently waiting session on top and the stopped ones at the foot", () => {
    const list = [
      s("stopped", { alive: false }),
      s("workingOld", { state: "working" }),
      s("askedFirst", { state: "question" }),
      s("askedLast", { state: "permission" }),
    ];
    const at = ledger({ askedFirst: 1_000, askedLast: 2_000 });
    expect(names(sortSessionsByAttention(list, at))).toEqual(["askedLast", "askedFirst", "workingOld", "stopped"]);
  });

  it("orders the live non-waiting ones by when they last waited, newest first", () => {
    // The session just answered (just) sits above one that has been running silently.
    const list = [s("never", { state: "working" }), s("old", { state: "idle" }), s("just", { state: "working" })];
    const at = ledger({ just: 5_000, old: 100 });
    expect(names(sortSessionsByAttention(list, at))).toEqual(["just", "old", "never"]);
  });

  it("falls back to newest-created when nothing ever waited", () => {
    const list = [
      s("old", { state: "working", createdAt: "2026-08-01T00:00:00Z" }),
      s("new", { state: "working", createdAt: "2026-09-01T00:00:00Z" }),
    ];
    expect(names(sortSessionsByAttention(list, ledger({})))).toEqual(["new", "old"]);
  });

  it("keeps a stopped session that carried an unanswered question ahead of the plain stopped ones", () => {
    const list = [
      s("plain", { alive: false, createdAt: "2026-09-01T00:00:00Z" }),
      s("carried", { alive: false, carried: "question", createdAt: "2026-08-01T00:00:00Z" }),
    ];
    expect(names(sortSessionsByAttention(list, ledger({})))).toEqual(["carried", "plain"]);
  });

  it("does not reorder its input", () => {
    const list = [s("b", { alive: false }), s("a", { state: "question" })];
    sortSessionsByAttention(list, ledger({ a: 1 }));
    expect(names(list)).toEqual(["b", "a"]);
  });
});

describe("waitingAtFromNotifications", () => {
  const n = (kind: string, id: string, createdAt: string, type = "session") => ({
    kind,
    target: { type, id },
    createdAt,
  });

  it("takes the newest waiting-for-a-person notification per session", () => {
    const at = waitingAtFromNotifications([
      n("question", "s1", "2026-09-01T10:00:00Z"),
      n("permission-request", "s1", "2026-09-01T12:00:00Z"),
      n("plan-approval", "s2", "2026-09-01T09:00:00Z"),
    ]);
    expect(at.s1).toBe(Date.parse("2026-09-01T12:00:00Z"));
    expect(at.s2).toBe(Date.parse("2026-09-01T09:00:00Z"));
  });

  it("ignores notifications that are not a session waiting for a person", () => {
    const at = waitingAtFromNotifications([
      n("answer-ready", "s1", "2026-09-01T10:00:00Z"), // the answer came back = not waiting
      n("question", "sched1", "2026-09-01T10:00:00Z", "schedule"), // not aimed at a session
      n("question", "s2", "not-a-date"),
    ]);
    expect(at).toEqual({});
  });
});
