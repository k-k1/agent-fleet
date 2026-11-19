import { describe, expect, it } from "vitest";
import { BAR_STEP_DIST, BAR_SWIPE_UP_IS_NEXT, barFits, barSwipeDelta, stepView } from "./barSwipe.ts";

const up = BAR_SWIPE_UP_IS_NEXT ? 1 : -1;

describe("barSwipeDelta", () => {
  it("swipe up is next and down is previous (the flip constant decides)", () => {
    expect(barSwipeDelta(0, -BAR_STEP_DIST - 1)).toBe(up);
    expect(barSwipeDelta(0, BAR_STEP_DIST + 1)).toBe(-up);
  });
  it("needs about one icon of travel", () => {
    expect(barSwipeDelta(0, -(BAR_STEP_DIST - 1))).toBe(0);
    expect(barSwipeDelta(0, -BAR_STEP_DIST)).toBe(up);
  });
  it("ignores a drag that is not mostly vertical", () => {
    expect(barSwipeDelta(60, -60)).toBe(0);
    expect(barSwipeDelta(-80, 50)).toBe(0);
  });
});

describe("stepView", () => {
  const ids = ["sessions", "assistant", "workitems", "memo", "files"];
  it("steps by one", () => {
    expect(stepView(ids, "assistant", 1)).toBe("workitems");
    expect(stepView(ids, "assistant", -1)).toBe("sessions");
  });
  it("clamps at both ends instead of wrapping", () => {
    expect(stepView(ids, "files", 1)).toBeNull();
    expect(stepView(ids, "sessions", -1)).toBeNull();
  });
  it("walks the list it is given, so a skipped Schedules is never a target", () => {
    expect(stepView(ids, "memo", 1)).toBe("files");
    const withSched = [...ids.slice(0, 4), "schedules", "files"];
    expect(stepView(withSched, "memo", 1)).toBe("schedules");
  });
  it("starts from the first entry when the current view is not listed", () => {
    expect(stepView(ids, "schedules", 1)).toBe("sessions");
  });
  it("never moves in a single-entry list", () => {
    expect(stepView(["sessions"], "sessions", 1)).toBeNull();
    expect(stepView(["sessions"], "sessions", -1)).toBeNull();
  });
  it("does nothing for an empty list or no delta", () => {
    expect(stepView([], "x", 1)).toBeNull();
    expect(stepView(ids, "memo", 0)).toBeNull();
  });
});

describe("barFits (overflow rule)", () => {
  it("fits when content is no taller than the box, with 1px tolerance", () => {
    expect(barFits({ scrollHeight: 400, clientHeight: 400 })).toBe(true);
    expect(barFits({ scrollHeight: 401, clientHeight: 400 })).toBe(true);
  });
  it("does not fit once it scrolls", () => {
    expect(barFits({ scrollHeight: 402, clientHeight: 400 })).toBe(false);
  });
});
