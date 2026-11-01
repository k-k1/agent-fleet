import { describe, expect, it } from "vitest";
import { normalizeRegionFormat, normalizeTimeZone } from "./normalize.ts";
import { defaultSetting } from "./prefs.ts";

describe("region format / time zone preferences (#2185)", () => {
  it("both default to the distinct stored value \"auto\", not today's zone", () => {
    expect(defaultSetting("regionFormat")).toBe("auto");
    expect(defaultSetting("timeZone")).toBe("auto");
  });
  it("normalizes junk to auto", () => {
    expect(normalizeRegionFormat("xx-YY")).toBe("auto");
    expect(normalizeRegionFormat(5)).toBe("auto");
    expect(normalizeRegionFormat("en-GB")).toBe("en-GB");
    expect(normalizeTimeZone(undefined)).toBe("auto");
    expect(normalizeTimeZone("")).toBe("auto");
  });
  it("keeps a zone name this browser may not know", () => {
    expect(normalizeTimeZone("Asia/Tokyo")).toBe("Asia/Tokyo");
    expect(normalizeTimeZone("Mars/Olympus_Mons")).toBe("Mars/Olympus_Mons");
  });
});
