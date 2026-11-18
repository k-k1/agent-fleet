import { describe, it, expect } from "vitest";
import { figure, fmtBytes, fmtPct } from "./usageHistory.ts";

describe("usage history figures", () => {
  it("keeps null apart from 0", () => {
    expect(figure(null, fmtPct)).toBeNull();
    expect(figure(undefined, fmtPct)).toBeNull();
    expect(figure(0, fmtPct)).toBe("0.0%");
  });
  it("formats bytes", () => {
    expect(fmtBytes(536870912)).toBe("512 MiB");
    expect(fmtBytes(3 * 1024 ** 3)).toBe("3.0 GiB");
  });
});
