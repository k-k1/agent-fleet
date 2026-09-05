import { describe, it, expect, beforeEach } from "vitest";
import { relTime, fmtDateTime, fmtNum, compareText, TIME_HM } from "./intl.ts";
import { setLocale } from "./i18n/index.ts";

const NOW = 1_700_000_000_000; // fixed reference; passed to relTime as `now` so tests are deterministic

describe("intl.relTime", () => {
  beforeEach(() => setLocale("ja"));

  it("returns just-now under 60s (localized)", () => {
    expect(relTime(NOW - 30_000, NOW)).toBe("たった今");
    setLocale("en");
    expect(relTime(NOW - 30_000, NOW)).toBe("just now");
  });

  it("formats past minutes/hours per locale", () => {
    expect(relTime(NOW - 3 * 60_000, NOW)).toMatch(/3.*分.*前/); // ja: "3 分前"
    setLocale("en");
    expect(relTime(NOW - 3 * 60_000, NOW)).toBe("3 minutes ago");
    expect(relTime(NOW - 2 * 3_600_000, NOW)).toBe("2 hours ago");
  });

  it("formats future instants", () => {
    setLocale("en");
    expect(relTime(NOW + 3_600_000, NOW)).toBe("in 1 hour");
  });

  it("returns empty for invalid / nullish input", () => {
    expect(relTime("not-a-date", NOW)).toBe("");
    expect(relTime(null, NOW)).toBe("");
    expect(relTime(undefined, NOW)).toBe("");
    expect(relTime("", NOW)).toBe("");
  });
});

describe("intl.fmtDateTime", () => {
  beforeEach(() => setLocale("ja"));

  it("renders a M/D HH:MM style string (TZ-independent structure)", () => {
    const out = fmtDateTime(NOW);
    expect(out).toMatch(/\d+\/\d+/); // has a M/D part
    expect(out).toMatch(/\d{2}:\d{2}/); // has a HH:MM part
  });

  it("TIME_HM renders HH:MM only", () => {
    expect(fmtDateTime(NOW, TIME_HM)).toMatch(/^\d{2}:\d{2}$/);
  });

  it("passes through an unparseable string and empties other invalid input", () => {
    expect(fmtDateTime("nope")).toBe("nope");
    expect(fmtDateTime(NaN)).toBe("");
  });
});

describe("intl.fmtNum", () => {
  it("groups thousands (comma in both ja and en)", () => {
    setLocale("ja");
    expect(fmtNum(1234567)).toBe("1,234,567");
    setLocale("en");
    expect(fmtNum(1234567)).toBe("1,234,567");
  });
});

describe("intl.compareText", () => {
  beforeEach(() => setLocale("en"));
  it("orders digit runs numerically (repo2 < repo10)", () => {
    expect(compareText("repo2", "repo10")).toBeLessThan(0);
    const arr = ["repo10", "repo2", "repo1"].sort(compareText);
    expect(arr).toEqual(["repo1", "repo2", "repo10"]);
  });
  it("is case/accent-insensitive for a stable name order", () => {
    expect(compareText("Alpha", "alpha")).toBe(0);
  });
  it("keeps RFC3339 timestamps in chronological order", () => {
    expect(compareText("2026-07-16T09:00:00Z", "2026-07-16T10:00:00Z")).toBeLessThan(0);
  });
});
