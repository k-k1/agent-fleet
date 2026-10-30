import { describe, expect, it } from "vitest";
import { deadlineFrom, fmtCountdown } from "./countdown.ts";

describe("cloud login countdown", () => {
  it("anchors the Agent's seconds left at receipt, and ignores anything that is not a count", () => {
    expect(deadlineFrom(90, 1_000)).toBe(91_000);
    expect(deadlineFrom(0, 1_000)).toBe(1_000);
    for (const bad of [undefined, null, "90", -1, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(deadlineFrom(bad, 1_000)).toBeNull();
    }
  });

  it("formats m:ss, and h:mm:ss from an hour up", () => {
    expect(fmtCountdown(0)).toBe("0:00");
    expect(fmtCountdown(-5)).toBe("0:00");
    expect(fmtCountdown(59.9)).toBe("0:59");
    expect(fmtCountdown(900)).toBe("15:00");
    expect(fmtCountdown(3600)).toBe("1:00:00");
    expect(fmtCountdown(3725)).toBe("1:02:05");
  });
});
