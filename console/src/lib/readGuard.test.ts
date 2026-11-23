import { describe, expect, it } from "vitest";
import { ReadGuard } from "./readGuard.ts";

describe("ReadGuard", () => {
  it("drops a read started before a reset (the subject changed); the next read lands", () => {
    const g = new ReadGuard();
    const old = g.begin();
    expect(old.land()).toBe(true);
    g.reset();
    const now = g.begin();
    expect(old.current()).toBe(false);
    expect(old.land()).toBe(false);
    expect(now.land()).toBe(true);
  });

  it("hands a dead ticket to a caller made for another subject", () => {
    const g = new ReadGuard();
    g.reset("a");
    const forA = g.begin("a");
    g.reset("b");
    const lateA = g.begin("a"); // e.g. a rename's refresh that closed over folder a
    expect(forA.current()).toBe(false);
    expect(lateA.current()).toBe(false);
    expect(lateA.land()).toBe(false);
    expect(lateA.signal.aborted).toBe(true);
    expect(g.begin("b").land()).toBe(true);
  });
  it("drops an older answer once a newer read has landed", () => {
    const g = new ReadGuard();
    const first = g.begin();
    const second = g.begin();
    expect(second.land()).toBe(true);
    expect(first.fresh()).toBe(false);
    expect(first.land()).toBe(false);
  });

  it("lands an older answer that is merely slow, while nothing newer has landed", () => {
    const g = new ReadGuard();
    const slow = g.begin();
    const tick = g.begin(); // still out
    expect(slow.land()).toBe(true);
    expect(tick.land()).toBe(true); // and the newer one still lands after it
  });

  it("keeps a mark per lane: a newer failure does not block an older success's list", () => {
    const g = new ReadGuard();
    const first = g.begin();
    const tick = g.begin();
    expect(tick.land("status")).toBe(true);
    expect(first.land("status")).toBe(false);
    expect(first.land("list")).toBe(true);
  });

  it("lets one read land the same lane twice", () => {
    const g = new ReadGuard();
    const t = g.begin();
    expect(t.land()).toBe(true);
    expect(t.fresh()).toBe(true);
    expect(t.land()).toBe(true);
  });

  it("aborts the signal of reads begun before a reset, not of later ones", () => {
    const g = new ReadGuard();
    const old = g.begin();
    g.reset();
    const now = g.begin();
    expect(old.signal.aborted).toBe(true);
    expect(now.signal.aborted).toBe(false);
  });

  it("gives a live ticket to a read begun from the old signal's abort listener", () => {
    const g = new ReadGuard();
    const old = g.begin();
    let next: ReturnType<ReadGuard["begin"]> | undefined;
    old.signal.addEventListener("abort", () => (next = g.begin()));
    g.reset();
    expect(next!.current()).toBe(true);
    expect(next!.signal.aborted).toBe(false);
    g.reset();
    expect(next!.signal.aborted).toBe(true);
  });

  it("claim drops every read already begun in the claimed lanes only", () => {
    const g = new ReadGuard();
    const before = g.begin();
    g.claim("rows");
    expect(before.land("rows")).toBe(false);
    expect(before.land("err")).toBe(true);
    const after = g.begin();
    expect(after.land("rows")).toBe(true);
  });

  it("claim works in the current subject and never revives a read of the previous one", () => {
    const g = new ReadGuard();
    g.reset("a");
    const t = g.begin("a");
    g.reset("b");
    g.claim("rows");
    expect(g.begin("b").land("rows")).toBe(true);
    expect(t.land("rows")).toBe(false);
  });
});
