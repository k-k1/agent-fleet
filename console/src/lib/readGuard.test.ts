import { describe, expect, it } from "vitest";
import { ReadGuard } from "./readGuard.ts";

describe("ReadGuard", () => {
  it("drops a read started before a reset (the subject changed)", () => {
    const g = new ReadGuard();
    const old = g.begin();
    g.reset();
    const now = g.begin();
    expect(old.current()).toBe(false);
    expect(old.land()).toBe(false);
    expect(now.land()).toBe(true);
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

  it("forgets the marks of the previous subject", () => {
    const g = new ReadGuard();
    g.begin(); // started before the reset, never answers
    const late = g.begin();
    expect(late.land()).toBe(true);
    g.reset();
    const next = g.begin();
    expect(next.land()).toBe(true);
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
});
