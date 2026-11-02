// Call sites that used to read the browser's zone (Date#getHours and friends) must follow the
// member's time zone. Each case fails on the old code because the test process runs in a zone
// other than the selected one.
import { afterEach, describe, expect, it } from "vitest";
import { setFormatPrefs } from "../../lib/intl.ts";
import { dayRange, localBucket, timezoneAlignsToHours, widenForTimezone } from "./uptime.ts";
import { lastLocalDays, uptimeWindow } from "../home/glance.ts";
import { studioStamp } from "../imagegen/studios.ts";
import { fmtDate } from "../settings/connect/tokenDate.ts";

const AT = "2026-08-19T14:50:00Z";

afterEach(() => setFormatPrefs("auto", "auto"));

describe("uptime bucketing", () => {
  it("localBucket maps a UTC hour into the selected zone", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(localBucket("2026-08-19T15")).toEqual({ day: "2026-08-20", hour: 0 });
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(localBucket("2026-08-19T05")).toEqual({ day: "2026-08-18", hour: 22 });
    expect(localBucket("garbage")).toBeNull();
  });
  it("timezoneAlignsToHours reads the selected zone", () => {
    setFormatPrefs("auto", "Asia/Kolkata");
    expect(timezoneAlignsToHours()).toBe(false);
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(timezoneAlignsToHours()).toBe(true);
  });
  it("dayRange / widenForTimezone do not depend on the zone", () => {
    setFormatPrefs("auto", "Pacific/Auckland");
    expect(dayRange("2026-09-26", "2026-09-29")).toEqual(["2026-09-26", "2026-09-27", "2026-09-28", "2026-09-29"]);
    expect(dayRange("x", "2026-09-29")).toEqual([]);
    expect(widenForTimezone("2026-09-26", "2026-09-29")).toEqual({ from: "2026-09-25", to: "2026-09-30" });
  });
  it("lastLocalDays / uptimeWindow count days in the selected zone, across DST", () => {
    // 2026-11-02 00:30 in New York (clocks went back on 11-01) is 05:30 UTC; in Tokyo it is 14:30 on 11-02.
    const now = Date.parse("2026-11-02T05:30:00Z");
    setFormatPrefs("auto", "America/New_York");
    expect(lastLocalDays(now, 3)).toEqual(["2026-10-31", "2026-11-01", "2026-11-02"]);
    // 2026-11-01 23:30 in New York is already 11-02 in Tokyo; the two zones disagree on today.
    const late = Date.parse("2026-11-02T03:30:00Z");
    expect(lastLocalDays(late, 2)).toEqual(["2026-10-31", "2026-11-01"]);
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(lastLocalDays(late, 2)).toEqual(["2026-11-01", "2026-11-02"]);
    expect(uptimeWindow(late)).toEqual({ from: "2026-10-26", to: "2026-11-03" });
  });
});

describe("display stamps", () => {
  it("studioStamp", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(studioStamp(AT)).toBe("8/19 23:50");
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(studioStamp(AT)).toBe("8/19 07:50");
    expect(studioStamp("nope")).toBe("");
    expect(studioStamp(undefined)).toBe("");
  });
  it("token fmtDate", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(fmtDate("2026-08-19T15:30:00Z")).toBe("2026-08-20");
    setFormatPrefs("auto", "UTC");
    expect(fmtDate("2026-08-19T15:30:00Z")).toBe("2026-08-19");
    expect(fmtDate("nope")).toBe("");
  });
});
