import { describe, it, expect } from "vitest";
import { fmtGiB, GiB } from "./bytes.ts";

describe("fmtGiB", () => {
  it("keeps 2 decimals under 10 GiB", () => {
    expect(fmtGiB(0.98 * GiB)).toBe("0.98");
    expect(fmtGiB(9.994 * GiB)).toBe("9.99");
  });
  it("compacts to 1 decimal from 10 GiB", () => {
    expect(fmtGiB(26.9 * GiB)).toBe("26.9");
  });
});
