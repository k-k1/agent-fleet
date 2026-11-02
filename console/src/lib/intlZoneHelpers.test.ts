// Zone-aware helpers in lib/intl.ts: day keys, stamps and offsets in the selected zone.
import { afterEach, describe, expect, it } from "vitest";
import { setFormatPrefs, shiftDayKey, zoneOffsetMinutes, zonedDayKey, zonedMdHm } from "./intl.ts";

// 2026-08-19 14:50 UTC = 23:50 on 08/19 in Tokyo, 07:50 on 08/19 in Los Angeles.
const AT = "2026-08-19T14:50:00Z";

afterEach(() => setFormatPrefs("auto", "auto"));

describe("zone helpers", () => {
  it("zonedDayKey / zonedMdHm", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(zonedDayKey("2026-08-19T15:30:00Z")).toBe("2026-08-20");
    expect(zonedMdHm(AT)).toBe("08-19 23:50");
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(zonedDayKey("2026-08-19T05:30:00Z")).toBe("2026-08-18");
    expect(zonedMdHm(AT)).toBe("08-19 07:50");
    expect(zonedDayKey("nope")).toBe("");
  });
  it("shiftDayKey is calendar arithmetic", () => {
    expect(shiftDayKey("2026-03-01", -1)).toBe("2026-02-28");
    expect(shiftDayKey("2026-12-31", 1)).toBe("2027-01-01");
    expect(shiftDayKey("bad", 1)).toBe("bad");
  });
  it("zoneOffsetMinutes follows the selected zone", () => {
    setFormatPrefs("auto", "Asia/Tokyo");
    expect(zoneOffsetMinutes(AT)).toBe(540);
    setFormatPrefs("auto", "Asia/Kolkata");
    expect(zoneOffsetMinutes(AT)).toBe(330);
    setFormatPrefs("auto", "America/Los_Angeles");
    expect(zoneOffsetMinutes(AT)).toBe(-420);
  });
});
