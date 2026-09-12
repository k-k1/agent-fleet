import { describe, expect, it } from "vitest";
import { MIRROR_POLL_FAST, MIRROR_POLL_REST, pollDelay } from "./pollCadence.ts";

describe("pollDelay", () => {
  it("holds the fast cadence through a turn that keeps producing", () => {
    // A streaming turn resets `unchanged` on every new line, so it never leaves rung one.
    expect(pollDelay({ working: true, unchanged: 0 })).toBe(MIRROR_POLL_FAST);
    expect(pollDelay({ working: true, unchanged: 7 })).toBe(MIRROR_POLL_FAST);
  });

  it("eases off a silent turn, but never past the resting cadence", () => {
    expect(pollDelay({ working: true, unchanged: 8 })).toBe(2000);
    expect(pollDelay({ working: true, unchanged: 24 })).toBe(2000);
    expect(pollDelay({ working: true, unchanged: 25 })).toBe(MIRROR_POLL_REST);
    expect(pollDelay({ working: true, unchanged: 10_000 })).toBe(MIRROR_POLL_REST);
  });

  it("eases off a resting session further, and stops at 15s", () => {
    expect(pollDelay({ working: false, unchanged: 0 })).toBe(MIRROR_POLL_REST);
    expect(pollDelay({ working: false, unchanged: 4 })).toBe(MIRROR_POLL_REST);
    expect(pollDelay({ working: false, unchanged: 5 })).toBe(8000);
    expect(pollDelay({ working: false, unchanged: 19 })).toBe(8000);
    expect(pollDelay({ working: false, unchanged: 20 })).toBe(15000);
    expect(pollDelay({ working: false, unchanged: 10_000 })).toBe(15000);
  });

  it("never returns slower than the rest ladder for a working session", () => {
    for (let n = 0; n < 200; n++) {
      expect(pollDelay({ working: true, unchanged: n })).toBeLessThanOrEqual(pollDelay({ working: false, unchanged: n }));
    }
  });

  it("is monotonic — more silence is never polled faster", () => {
    for (const working of [true, false]) {
      let prev = 0;
      for (let n = 0; n < 200; n++) {
        const ms = pollDelay({ working, unchanged: n });
        expect(ms).toBeGreaterThanOrEqual(prev);
        prev = ms;
      }
    }
  });

  it("treats a negative streak as none (a reset racing a tick)", () => {
    expect(pollDelay({ working: true, unchanged: -3 })).toBe(MIRROR_POLL_FAST);
    expect(pollDelay({ working: false, unchanged: -3 })).toBe(MIRROR_POLL_REST);
  });
});
