import { describe, expect, it } from "vitest";
import {
  DRAIN_SETTLE_MS, editItem, enqueue, injectsMidTurn, insertAt, moveItem, removeItem, shouldDrain, type DrainState,
  type QueuedSend,
} from "./queue.ts";

const q = (id: string, text = id, paths: string[] = []): QueuedSend => ({ id, text, paths });
const ids = (xs: QueuedSend[]) => xs.map((x) => x.id).join(",");

describe("queue edits", () => {
  it("keeps FIFO order on enqueue", () => {
    expect(ids(enqueue(enqueue([], q("a")), q("b")))).toBe("a,b");
  });
  it("removes, and leaves the same list for an unknown id", () => {
    const xs = [q("a"), q("b")];
    expect(ids(removeItem(xs, "a"))).toBe("b");
    expect(removeItem(xs, "zz")).toBe(xs);
  });
  it("moves one place and stops at the ends", () => {
    const xs = [q("a"), q("b"), q("c")];
    expect(ids(moveItem(xs, "c", -1))).toBe("a,c,b");
    expect(ids(moveItem(xs, "a", 1))).toBe("b,a,c");
    expect(moveItem(xs, "a", -1)).toBe(xs);
    expect(moveItem(xs, "c", 1)).toBe(xs);
  });
  it("edits text, trims it, and refuses to empty a text-only item", () => {
    const xs = [q("a", "old")];
    expect(editItem(xs, "a", "  new ")[0].text).toBe("new");
    expect(editItem(xs, "a", "   ")).toBe(xs);
    expect(editItem([q("a", "", ["/p"])], "a", "")[0].paths).toEqual(["/p"]);
  });
  it("puts a failed item back where it was, clamped", () => {
    expect(ids(insertAt([q("b"), q("c")], q("a"), 0))).toBe("a,b,c");
    expect(ids(insertAt([q("a")], q("z"), 9))).toBe("a,z");
  });
});

describe("shouldDrain", () => {
  const base: DrainState = { count: 2, busy: false, canSend: true, paused: false, inflight: false, awaitingSince: null, now: 1_000_000 };
  it("releases the head on idle", () => expect(shouldDrain(base)).toBe(true));
  it.each([
    ["empty", { count: 0 }],
    ["busy", { busy: true }],
    ["cannot send", { canSend: false }],
    ["paused by a stop", { paused: true }],
    ["a send in flight", { inflight: true }],
  ])("holds when %s", (_n, over) => expect(shouldDrain({ ...base, ...over })).toBe(false));
  it("does not release a second item before busy was seen after the first (idle→working between items)", () => {
    const s = { ...base, awaitingSince: base.now - 100 };
    expect(shouldDrain(s)).toBe(false);
    expect(shouldDrain({ ...s, now: s.now + DRAIN_SETTLE_MS })).toBe(true);
  });
});

describe("injectsMidTurn", () => {
  it("promises mid-turn injection only for managed codex and muse", () => {
    expect(injectsMidTurn("codex", true)).toBe(true);
    expect(injectsMidTurn("muse", true)).toBe(true);
    expect(injectsMidTurn("codex", false)).toBe(false);
    for (const k of ["claude", "opencode", "cursor", "copilot", "kiro", "lcpp", "agy"]) expect(injectsMidTurn(k, true)).toBe(false);
  });
});
