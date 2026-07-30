import { describe, it, expect } from "vitest";
import { wheelScrollDelta } from "./dragScroll.ts";

// A stand-in for the chip row (node env has no DOM): 300px wide viewport over a
// 900px track → max scrollLeft 600.
const row = (scrollLeft = 0, scrollWidth = 900) => ({ scrollLeft, scrollWidth, clientWidth: 300 });
const wheel = (d: Partial<{ deltaX: number; deltaY: number; deltaMode: number; ctrlKey: boolean }>) => ({
  deltaX: 0,
  deltaY: 0,
  deltaMode: 0,
  ctrlKey: false,
  ...d,
});

describe("wheelScrollDelta", () => {
  it("turns a vertical wheel notch into horizontal px", () => {
    expect(wheelScrollDelta(wheel({ deltaY: 120 }), row(0))).toBe(120);
    expect(wheelScrollDelta(wheel({ deltaY: -120 }), row(200))).toBe(-120);
  });

  it("passes through when the row fits (nothing to scroll)", () => {
    expect(wheelScrollDelta(wheel({ deltaY: 120 }), row(0, 300))).toBe(0);
    expect(wheelScrollDelta(wheel({ deltaY: 120 }), row(0, 120))).toBe(0);
  });

  it("clamps to the remaining range and releases the event at the ends", () => {
    expect(wheelScrollDelta(wheel({ deltaY: 120 }), row(540))).toBe(60); // 600 - 540
    expect(wheelScrollDelta(wheel({ deltaY: 120 }), row(600))).toBe(0); // right end → parent scrolls
    expect(wheelScrollDelta(wheel({ deltaY: -120 }), row(40))).toBe(-40);
    expect(wheelScrollDelta(wheel({ deltaY: -120 }), row(0))).toBe(0); // left end
  });

  it("leaves horizontal-dominant wheels and Ctrl+wheel (zoom) alone", () => {
    expect(wheelScrollDelta(wheel({ deltaX: 80, deltaY: 10 }), row(100))).toBe(0);
    expect(wheelScrollDelta(wheel({ deltaY: 120, ctrlKey: true }), row(100))).toBe(0);
    expect(wheelScrollDelta(wheel({ deltaY: 0 }), row(100))).toBe(0);
  });

  it("scales line and page delta modes", () => {
    expect(wheelScrollDelta(wheel({ deltaY: 3, deltaMode: 1 }), row(0))).toBe(48); // 3 lines * 16px
    expect(wheelScrollDelta(wheel({ deltaY: 1, deltaMode: 2 }), row(0))).toBe(300); // one viewport
  });
});
