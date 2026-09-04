// The ledger of "when a session last started waiting on a human". It feeds the ordering, so a
// wrong entry makes the top of the palette lie. In particular a first observation is never
// stamped: we do not know when it entered, and burning `now` would line every session up at the
// same time after a reload, destroying the real order held by the notification ledger.
import { beforeEach, describe, expect, it, vi } from "vitest";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});

import { isWaiting, noteSessions, observedWaitingAt, resetWaitingLedgerForTest } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

const s = (name: string, state: string, alive = true): Session => ({ name, kind: "claude", alive, state });

describe("waiting ledger", () => {
  beforeEach(() => {
    values.clear();
    resetWaitingLedgerForTest();
  });

  it("counts only a live question/plan/permission as waiting on a human", () => {
    expect(isWaiting(s("a", "question"))).toBe(true);
    expect(isWaiting(s("a", "plan"))).toBe(true);
    expect(isWaiting(s("a", "permission"))).toBe(true);
    expect(isWaiting(s("a", "working"))).toBe(false);
    expect(isWaiting(s("a", "question", false))).toBe(false); // stopped: answering it now moves nothing
  });

  it("does not stamp a session that was ALREADY waiting when first seen", () => {
    noteSessions([s("a", "question")], 1_000);
    expect(observedWaitingAt("a")).toBe(0);
  });

  it("stamps the transition into waiting, and only that transition", () => {
    noteSessions([s("a", "working")], 1_000);
    noteSessions([s("a", "question")], 2_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    // While it keeps waiting the stamp is when it entered, not now.
    noteSessions([s("a", "question")], 3_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    // Answered, moved on, asked again: the stamp updates to the second time — the gap the
    // notification ledger alone would leave stale.
    noteSessions([s("a", "working")], 4_000);
    noteSessions([s("a", "plan")], 5_000);
    expect(observedWaitingAt("a")).toBe(5_000);
  });

  it("survives a reload through localStorage", () => {
    noteSessions([s("a", "working")], 1_000);
    noteSessions([s("a", "question")], 2_000);
    resetWaitingLedgerForTest(); // = a new tab / a reload
    expect(observedWaitingAt("a")).toBe(2_000);
  });

  it("forgets sessions that left the list, but never on an empty list", () => {
    noteSessions([s("a", "working"), s("b", "working")], 1_000);
    noteSessions([s("a", "question"), s("b", "question")], 2_000);
    // An empty list is not "everything is gone" — a failed fetch and the startup value are
    // both empty.
    noteSessions([], 3_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    expect(observedWaitingAt("b")).toBe(2_000);
    // When a list arrives without b, only b is dropped.
    noteSessions([s("a", "question")], 4_000);
    expect(observedWaitingAt("a")).toBe(2_000);
    expect(observedWaitingAt("b")).toBe(0);
  });

  it("ignores a corrupted stored value instead of throwing", () => {
    values.set("af.sessionWaitingAt", "{not json");
    expect(observedWaitingAt("a")).toBe(0);
  });
});
