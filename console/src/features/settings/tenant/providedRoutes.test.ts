// The provided-route month's reset (ADR 0129 D4): the first of the next month, 00:00 UTC.
import { describe, it, expect } from "vitest";
import { resetOf } from "./providedRoutes.ts";

describe("resetOf", () => {
  it.each([
    ["2026-11", "2026-12-01T00:00:00.000Z"],
    ["2026-12", "2027-01-01T00:00:00.000Z"],
    ["2027-01", "2027-02-01T00:00:00.000Z"],
  ])("%s resets at %s", (month, at) => {
    expect(resetOf(month)?.toISOString()).toBe(at);
  });

  it("shows no reset for a month it cannot read", () => {
    for (const bad of ["", "2026-13", "2026-00", "2026-1", "2026-11-01", "Nov 2026"]) expect(resetOf(bad), bad).toBeNull();
  });
});
