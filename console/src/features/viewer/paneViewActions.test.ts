import { describe, it, expect } from "vitest";
import { registerPaneViewActions, paneViewActions } from "./paneViewActions.ts";

describe("paneViewActions registry", () => {
  it("registers, looks up, and clears a pane's actions", () => {
    const a = { toggleMdMode: () => {} };
    const off = registerPaneViewActions("p1", a);
    expect(paneViewActions("p1")).toBe(a);
    expect(paneViewActions("nope")).toBeUndefined();
    off();
    expect(paneViewActions("p1")).toBeUndefined();
  });

  it("the cleanup is identity-guarded: a late unmount can't clobber a remount", () => {
    const first = { toggleMdMode: () => {} };
    const offFirst = registerPaneViewActions("p1", first);
    // Same pane remounts and re-registers before the first mount's cleanup runs.
    const second = { toggleMdMode: () => {} };
    registerPaneViewActions("p1", second);
    offFirst(); // stale cleanup — must NOT remove the newer registration
    expect(paneViewActions("p1")).toBe(second);
  });

  it("isolates actions per pane id", () => {
    const a = { toggleMdMode: () => {} };
    const b = { toggleMdMode: () => {} };
    registerPaneViewActions("pa", a);
    registerPaneViewActions("pb", b);
    expect(paneViewActions("pa")).toBe(a);
    expect(paneViewActions("pb")).toBe(b);
  });
});
