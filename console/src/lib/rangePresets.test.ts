import { describe, it, expect } from "vitest";
import { matchPreset, presetRange, todayKey } from "./rangePresets.ts";

const at = (iso: string) => new Date(iso);

describe("presetRange (UTC)", () => {
  it("this month runs from the 1st to today", () => {
    expect(presetRange("this_month", at("2026-11-06T12:00:00Z"), "utc")).toEqual({ from: "2026-11-01", to: "2026-11-06" });
  });
  it("last month is the whole previous month", () => {
    expect(presetRange("last_month", at("2026-11-06T12:00:00Z"), "utc")).toEqual({ from: "2026-10-01", to: "2026-10-31" });
    expect(presetRange("last_month", at("2026-05-31T23:59:59Z"), "utc")).toEqual({ from: "2026-04-01", to: "2026-04-30" });
  });
  it("January steps back to the previous December", () => {
    expect(presetRange("last_month", at("2027-01-15T00:00:00Z"), "utc")).toEqual({ from: "2026-12-01", to: "2026-12-31" });
  });
  it("March after a leap February ends on the 29th, after a common one on the 28th", () => {
    expect(presetRange("last_month", at("2028-03-10T00:00:00Z"), "utc")).toEqual({ from: "2028-02-01", to: "2028-02-29" });
    expect(presetRange("last_month", at("2027-03-10T00:00:00Z"), "utc")).toEqual({ from: "2027-02-01", to: "2027-02-28" });
  });
  it("on the 1st, this month is a single day", () => {
    expect(presetRange("this_month", at("2026-11-01T00:00:00Z"), "utc")).toEqual({ from: "2026-11-01", to: "2026-11-01" });
  });
  it("last 30 days is 30 inclusive days and crosses a year boundary", () => {
    expect(presetRange("last_30", at("2027-01-10T00:00:00Z"), "utc")).toEqual({ from: "2026-12-12", to: "2027-01-10" });
  });
  it("uses the UTC date, not the machine's: 23:30 UTC on Oct 31 is still October", () => {
    expect(todayKey(at("2026-10-31T23:30:00Z"), "utc")).toBe("2026-10-31");
    expect(presetRange("this_month", at("2026-10-31T23:30:00Z"), "utc").from).toBe("2026-10-01");
  });
});

describe("matchPreset", () => {
  const now = at("2026-11-06T12:00:00Z");
  it("recognises a preset range and falls back to custom", () => {
    expect(matchPreset("2026-10-01", "2026-10-31", now, "utc")).toBe("last_month");
    expect(matchPreset("2026-11-01", "2026-11-06", now, "utc")).toBe("this_month");
    expect(matchPreset("2026-10-02", "2026-10-31", now, "utc")).toBe("custom");
    expect(matchPreset("", "", now, "utc")).toBe("custom");
  });
});
