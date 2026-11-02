import { afterEach, describe, expect, it } from "vitest";
import { setFormatPrefs } from "../../lib/intl.ts";
import { fullLocal, shortLocal } from "./read.ts";

// 2026-08-19 14:50 UTC = 23:50 on 08/19 in Tokyo, 07:50 on 08/19 in Los Angeles.
const AT = "2026-08-19T14:50:00Z";

afterEach(() => setFormatPrefs("auto", "auto"));

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
