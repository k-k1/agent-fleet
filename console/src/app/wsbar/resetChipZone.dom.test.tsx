// resetChipText decides "same day" in the member's time zone, not the browser's. It lives in a
// dom test because usageCommon.tsx pulls in the api client, which needs localStorage.
import { afterEach, expect, it, vi } from "vitest";
import { setFormatPrefs } from "../../lib/intl.ts";
import { resetChipText } from "./usageCommon.tsx";

afterEach(() => {
  setFormatPrefs("auto", "auto");
  vi.useRealTimers();
});

it("resetChipText: same day in the selected zone drops the date", () => {
  // Now 2026-08-19 14:00Z: 23:00 on 08/19 in Tokyo. A reset at 15:30Z is 00:30 on 08/20 there.
  vi.useFakeTimers({ now: Date.parse("2026-08-19T14:00:00Z") });
  setFormatPrefs("en-US", "Asia/Tokyo");
  expect(resetChipText("2026-08-19T15:30:00Z")).toContain("8/20");
  setFormatPrefs("en-US", "UTC");
  expect(resetChipText("2026-08-19T15:30:00Z")).toBe("15:30");
});
