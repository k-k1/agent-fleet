import { describe, expect, it } from "vitest";
import { markSeenIn, parseSeen, SEEN_MAX, seedSeen, unreadOf, type SeenMap } from "./seen.ts";
import type { WorkItem } from "./read.ts";

const item = (key: string, updatedAt: string, comments: number | null = 0): WorkItem =>
  ({ provider: "github", key, updatedAt, commentCount: comments }) as unknown as WorkItem;

describe("work item unread baseline (#2779)", () => {
  it("seeds on first sight so a first load marks nothing", () => {
    const items = [item("a/b#1", "2026-10-01T00:00:00Z", 2), item("a/b#2", "2026-10-02T00:00:00Z")];
    const m = seedSeen({}, items);
    expect(items.map((i) => unreadOf(m, i))).toEqual(["", ""]);
  });

  it("does not move a known baseline when seeding again", () => {
    const m = seedSeen({}, [item("a/b#1", "2026-10-01T00:00:00Z")]);
    expect(seedSeen(m, [item("a/b#1", "2026-10-09T00:00:00Z")])).toBe(m);
  });

  it("says updated when updatedAt moved, comments when the count also grew", () => {
    const m = seedSeen({}, [item("a/b#1", "2026-10-01T00:00:00Z", 2)]);
    expect(unreadOf(m, item("a/b#1", "2026-10-03T00:00:00Z", 2))).toBe("updated");
    expect(unreadOf(m, item("a/b#1", "2026-10-03T00:00:00Z", 3))).toBe("comments");
    expect(unreadOf(m, item("a/b#1", "2026-10-01T00:00:00Z", 5))).toBe("");
  });

  it("clears once the item is marked seen", () => {
    const newer = item("a/b#1", "2026-10-03T00:00:00Z", 3);
    const m = markSeenIn(seedSeen({}, [item("a/b#1", "2026-10-01T00:00:00Z")]), newer);
    expect(unreadOf(m, newer)).toBe("");
  });

  it("evicts the oldest entries past the bound, keeping the touched one", () => {
    let m: SeenMap = {};
    for (let n = 0; n < SEEN_MAX + 20; n++) m = markSeenIn(m, item(`a/b#${n}`, "2026-10-01T00:00:00Z"));
    expect(Object.keys(m)).toHaveLength(SEEN_MAX);
    expect(m["github:a/b#0"]).toBeUndefined();
    expect(m[`github:a/b#${SEEN_MAX + 19}`]).toBeDefined();
    // Re-marking an old entry makes it the newest, so it outlives the next eviction.
    const keep = item(`a/b#${20}`, "2026-10-01T00:00:00Z");
    m = markSeenIn(m, keep);
    m = markSeenIn(m, item("a/b#new", "2026-10-01T00:00:00Z"));
    expect(m["github:a/b#20"]).toBeDefined();
  });

  it("does not read unknown -> known as new comments, and fills the count in quietly", () => {
    // Baseline taken from an older Agent's row (no count).
    const m = seedSeen({}, [item("a/b#1", "2026-10-01T00:00:00Z", null)]);
    expect(m["github:a/b#1"].c).toBeNull();
    // Upgraded Agent: count 7 appears with a label-edit updatedAt bump.
    expect(unreadOf(m, item("a/b#1", "2026-10-03T00:00:00Z", 7))).toBe("updated");
    // Without newer activity the count is adopted as the baseline...
    const m2 = seedSeen(m, [item("a/b#1", "2026-10-01T00:00:00Z", 7)]);
    expect(m2["github:a/b#1"].c).toBe(7);
    // ...so real growth after that is recognised, and a known 0 stays a number.
    expect(unreadOf(m2, item("a/b#1", "2026-10-04T00:00:00Z", 8))).toBe("comments");
    expect(seedSeen({}, [item("a/b#2", "2026-10-01T00:00:00Z", 0)])["github:a/b#2"].c).toBe(0);
    expect(parseSeen('{"github:x#1":{"u":"2026-10-01T00:00:00Z","c":null}}')["github:x#1"].c).toBeNull();
  });

  it("fills several unknown baselines in one pass", () => {
    const old = [item("a/b#1", "2026-10-01T00:00:00Z", null), item("a/b#2", "2026-10-01T00:00:00Z", null)];
    const m = seedSeen({}, old);
    const next = seedSeen(m, [
      item("a/b#1", "2026-10-01T00:00:00Z", 7),
      item("a/b#2", "2026-10-01T00:00:00Z", 4),
      item("a/b#9", "2026-10-01T00:00:00Z", 1),
    ]);
    expect([next["github:a/b#1"].c, next["github:a/b#2"].c, next["github:a/b#9"].c]).toEqual([7, 4, 1]);
    expect(unreadOf(next, item("a/b#2", "2026-10-05T00:00:00Z", 5))).toBe("comments");
  });

  it("survives corrupt storage", () => {
    expect(parseSeen("{not json")).toEqual({});
    expect(parseSeen("[1,2]")).toEqual({});
    expect(parseSeen("null")).toEqual({});
    expect(parseSeen(null)).toEqual({});
    expect(parseSeen('{"github:x#1":{"u":"2026-10-01T00:00:00Z","c":1},"github:x#2":{"u":5},"github:x#3":7}')).toEqual({
      "github:x#1": { u: "2026-10-01T00:00:00Z", c: 1 },
    });
  });
});
