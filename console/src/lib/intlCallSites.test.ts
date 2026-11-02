import { afterEach, describe, expect, it } from "vitest";
import { setFormatPrefs, zonedParts } from "./intl.ts";
import { resumeClock, stamp } from "./sessionview.ts";
import { fullLocal, shortLocal } from "../features/workitems/read.ts";

// 2026-08-19 14:50 UTC = 23:50 on 08/19 in Tokyo, 07:50 on 08/19 in Los Angeles.
const AT = "2026-08-19T14:50:00Z";

afterEach(() => setFormatPrefs("auto", "auto"));

describe("zonedParts", () => {
  it("reads the calendar fields in the chosen zone, not the browser's", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(zonedParts(AT)).toEqual({ year: 2026, month: 8, day: 19, hour: 23, minute: 50 });
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(zonedParts(AT)).toEqual({ year: 2026, month: 8, day: 19, hour: 7, minute: 50 });
  });
  it("keeps Latin digits and 00-23 hours under any region format", () => {
    setFormatPrefs("ar-EG", "UTC");
    expect(zonedParts("2026-08-19T00:05:00Z")).toEqual({ year: 2026, month: 8, day: 19, hour: 0, minute: 5 });
  });
  it("is null for garbage", () => {
    expect(zonedParts("nope")).toBeNull();
  });
});

describe("stamp / resumeClock follow the time zone", () => {
  it("stamp", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(stamp(AT)).toBe("0819-2350");
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(stamp(AT)).toBe("0819-0750");
  });
  it("resumeClock decides 'today' in the chosen zone", () => {
    const now = new Date("2026-08-19T14:00:00Z");
    // Tokyo: now is 23:00 on 08/19 and the resume instant is 00:30 on 08/20, so not today.
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(resumeClock("2026-08-19T15:30:00Z", now)).toBe("08/20 00:30");
    // UTC: both are on 08/19.
    setFormatPrefs("auto", "UTC");
    expect(resumeClock("2026-08-19T15:30:00Z", now)).toBe("15:30");
  });
});

describe("work item stamps", () => {
  it("auto keeps the shape of the old toLocaleString call (en-US, UTC)", () => {
    setFormatPrefs("en-US", "UTC");
    expect(fullLocal(AT)).toBe(
      new Date(AT).toLocaleString("en-US", { timeZone: "UTC", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }),
    );
    expect(shortLocal(AT)).toBe(
      new Date(AT).toLocaleString("en-US", { timeZone: "UTC", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }),
    );
  });
  it("time zone and region format apply", () => {
    setFormatPrefs("en-US", "Asia/Tokyo");
    expect(fullLocal(AT)).toBe("08/19/2026, 11:50 PM");
    expect(shortLocal(AT)).toBe("08/19, 11:50 PM");
    setFormatPrefs("ja-JP", "America/Los_Angeles");
    expect(fullLocal(AT)).toBe("2026/08/19 07:50");
  });
  it("garbage is echoed back as before", () => {
    expect(fullLocal("nope")).toBe("nope");
    expect(fullLocal("")).toBe("");
  });
});
