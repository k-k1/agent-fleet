import { describe, expect, it } from "vitest";
import { nextHomeDiskPressure } from "./homeDiskPressure.ts";

const GIB = 1024 ** 3;

describe("nextHomeDiskPressure", () => {
  it("raises on little free space or high usage", () => {
    expect(nextHomeDiskPressure(false, 99 * GIB, 100 * GIB)).toBe(true); // 1 GiB free
    expect(nextHomeDiskPressure(false, 920 * GIB, 1000 * GIB)).toBe(true); // 92%, plenty free
  });
  it("stays quiet with headroom", () => {
    expect(nextHomeDiskPressure(false, 50 * GIB, 100 * GIB)).toBe(false);
  });
  it("does not clear until real headroom is back (hysteresis)", () => {
    expect(nextHomeDiskPressure(true, 97.5 * GIB, 100 * GIB)).toBe(true); // 2.5 GiB free: above raise, below clear
    expect(nextHomeDiskPressure(true, 86 * GIB, 100 * GIB)).toBe(true); // 14 GiB free but 86%
    expect(nextHomeDiskPressure(true, 80 * GIB, 100 * GIB)).toBe(false);
  });
  it("ignores an unusable reading", () => {
    expect(nextHomeDiskPressure(true, 0, 0)).toBe(true);
    expect(nextHomeDiskPressure(false, 1, 0)).toBe(false);
  });
});
