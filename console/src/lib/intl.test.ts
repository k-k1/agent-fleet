import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  relTime,
  fmtDateTime,
  fmtNum,
  compareText,
  TIME_HM,
  setFormatPrefs,
  effectiveTimeZone,
  selectedTimeZone,
  browserTimeZone,
  formatLocale,
  isValidTimeZone,
} from "./intl.ts";
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

// Region format and time zone (#2185). Fixed instants only: 2026-03-08T07:30Z is the moment US
// clocks jump (02:00 -> 03:00 PST->PDT in America/Los_Angeles); 2026-03-29T01:30Z is the EU one.
describe("intl region format and time zone", () => {
  const US_DST_AFTER = Date.UTC(2026, 2, 8, 10, 30); // 03:30 PDT (UTC-7), 02:30 never existed
  const US_DST_BEFORE = Date.UTC(2026, 2, 8, 9, 30); // 01:30 PST (UTC-8)
  const HM = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" } as const;
  beforeEach(() => {
    setLocale("en");
    setFormatPrefs("auto", "auto");
  });
  afterEach(() => setFormatPrefs("auto", "auto"));

  it("shows the same instant in the chosen zone, across a DST jump", () => {
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(fmtDateTime(US_DST_BEFORE, HM)).toBe("01:30");
    expect(fmtDateTime(US_DST_AFTER, HM)).toBe("03:30");
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(fmtDateTime(US_DST_AFTER, HM)).toBe("19:30");
  });

  it("follows the browser's zone while the setting is auto, and re-reads it each call", () => {
    const spy = vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions");
    spy.mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
    expect(effectiveTimeZone()).toBe("Asia/Tokyo");
    expect(fmtDateTime(US_DST_AFTER, HM)).toBe("19:30");
    // The member travelled: same stored "auto", new browser zone, no stale cached formatter.
    spy.mockReturnValue({ timeZone: "America/Los_Angeles" } as Intl.ResolvedDateTimeFormatOptions);
    expect(effectiveTimeZone()).toBe("America/Los_Angeles");
    expect(fmtDateTime(US_DST_AFTER, HM)).toBe("03:30");
    spy.mockRestore();
  });

  it("falls back to the browser's zone for an unknown stored zone instead of throwing", () => {
    setFormatPrefs("auto", "Mars/Olympus_Mons");
    expect(selectedTimeZone()).toBeUndefined();
    expect(() => fmtDateTime(US_DST_AFTER, HM)).not.toThrow();
    expect(effectiveTimeZone()).toBe(browserTimeZone());
  });

  it("writes dates in the region format, independent of the display language", () => {
    const day = Date.UTC(2026, 10, 2, 12, 0);
    const D = { year: "numeric", month: "numeric", day: "numeric", timeZone: "UTC" } as const;
    setFormatPrefs("en-US", "auto");
    expect(fmtDateTime(day, D)).toBe("11/2/2026");
    setFormatPrefs("en-GB", "auto");
    expect(fmtDateTime(day, D)).toBe("02/11/2026");
    setFormatPrefs("de-DE", "auto");
    expect(fmtNum(1234567.5)).toBe("1.234.567,5");
    setFormatPrefs("auto", "auto");
    expect(fmtNum(1234567.5)).toBe("1,234,567.5"); // follows the display language (en)
  });

  it("treats an unusable region format as the display language", () => {
    setFormatPrefs("not a tag!!", "auto");
    expect(formatLocale()).toBe("en");
  });

  it("isValidTimeZone never throws", () => {
    expect(isValidTimeZone("Asia/Tokyo")).toBe(true);
    expect(isValidTimeZone("Nope/Zone")).toBe(false);
    expect(isValidTimeZone("")).toBe(false);
    expect(isValidTimeZone(42)).toBe(false);
  });
});
