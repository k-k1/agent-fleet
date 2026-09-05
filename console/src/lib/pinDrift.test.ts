import { describe, expect, it } from "vitest";
import { pinDrift } from "./pinDrift.ts";

// Pins the direction against version shapes measured in the field: extractVer's output in
// env_tool_versions, and the pins in versions.json.
describe("pinDrift", () => {
  it("behind: older than the pin (shapes seen when kiro got stuck)", () => {
    expect(pinDrift("2.14.1", "2.14.2")).toBe("behind");
    expect(pinDrift("1.18.5", "1.18.8")).toBe("behind");
    expect(pinDrift("2.1.219", "2.1.220")).toBe("behind");
  });

  it("ahead: newer than the pin (shapes seen when self-update was on)", () => {
    expect(pinDrift("1.18.8", "1.18.5")).toBe("ahead");
    expect(pinDrift("0.44.0", "0.43.0")).toBe("ahead");
    expect(pinDrift("1.1.8", "1.1.7")).toBe("ahead");
  });

  it("same: equal, so no badge", () => {
    expect(pinDrift("2.1.220", "2.1.220")).toBe("same");
    expect(pinDrift("1.0.75", "1.0.75")).toBe("same");
  });

  it("cursor's dated versions: ignore the pin's sha suffix and compare the date part", () => {
    // The effective value is "2026.07.23" after extractVer drops the suffix; the pin is the raw
    // value with the sha.
    expect(pinDrift("2026.07.23", "2026.07.23-e383d2b")).toBe("same");
    expect(pinDrift("2026.07.20", "2026.07.23-e383d2b")).toBe("behind");
    expect(pinDrift("2026.07.30", "2026.07.23-e383d2b")).toBe("ahead");
  });

  it("treats a missing segment as 0 when the segment counts differ", () => {
    expect(pinDrift("1.2", "1.2.3")).toBe("behind");
    expect(pinDrift("1.2.3", "1.2")).toBe("ahead");
    expect(pinDrift("1.2.0", "1.2")).toBe("same");
  });

  it("unknown: no badge for an undecidable shape", () => {
    expect(pinDrift("(取得失敗)", "1.1.7")).toBe("unknown"); // a host where agy hits the RDRAND SIGABRT
    expect(pinDrift("(timeout)", "1.1.7")).toBe("unknown");
    expect(pinDrift("", "1.1.7")).toBe("unknown");
    expect(pinDrift(undefined, "1.1.7")).toBe("unknown");
    expect(pinDrift("1.1.7", "")).toBe("unknown");
    expect(pinDrift("1.1.7", undefined)).toBe("unknown"); // tools with no pin (node/python)
  });
});
