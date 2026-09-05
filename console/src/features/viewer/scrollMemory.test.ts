// The position store itself: how keys are built, and what is evicted on overflow.
import { afterEach, describe, expect, it } from "vitest";
import { clearScrollPos, loadScrollPos, saveScrollPos, scrollMemoryKey } from "./scrollMemory.ts";

afterEach(() => clearScrollPos());

describe("scrollMemoryKey", () => {
  it("separates on both pane and file", () => {
    // The requirement: the same file open in two panes can be read at two places.
    expect(scrollMemoryKey("pane-1", "repos/x/a.go")).not.toBe(scrollMemoryKey("pane-2", "repos/x/a.go"));
    expect(scrollMemoryKey("pane-1", "repos/x/a.go")).not.toBe(scrollMemoryKey("pane-1", "repos/x/b.go"));
  });

  it("builds no key without a file, so nothing is remembered", () => {
    expect(scrollMemoryKey("pane-1", "")).toBeNull();
  });

  it("still builds a key without a pane id (pop-out and the like)", () => {
    expect(scrollMemoryKey(undefined, "repos/x/a.go")).not.toBeNull();
  });
});

describe("save/load", () => {
  it("returns the stored position as-is, and null when there is none", () => {
    saveScrollPos("k", 820);
    expect(loadScrollPos("k")).toBe(820);
    expect(loadScrollPos("other")).toBeNull();
  });

  it("distinguishes the top (0) from having no memory", () => {
    // Dropping 0 would fling anyone who read to the end and scrolled back to the top away to the
    // old position on every return.
    saveScrollPos("k", 500);
    saveScrollPos("k", 0);
    expect(loadScrollPos("k")).toBe(0);
  });

  it("evicts oldest first on overflow and keeps the recently used", () => {
    for (let i = 0; i < 200; i++) saveScrollPos(`k${i}`, i);
    saveScrollPos("k0", 999); // touched, so it goes back to "recent"
    saveScrollPos("new", 1);
    expect(loadScrollPos("k0")).toBe(999);
    expect(loadScrollPos("k1")).toBeNull(); // the oldest one fell out
    expect(loadScrollPos("new")).toBe(1);
  });
});
