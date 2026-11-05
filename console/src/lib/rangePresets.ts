// Range presets for the from/to bars (Usage, Cloud cost, uptime). Pure date math on YYYY-MM-DD
// keys so the clock is an argument and a test can pin it.
//
// Which calendar a preset follows is the caller's choice: the usage ledger and Cost Explorer
// bucket by UTC day, so their months are UTC months ("utc"); the uptime heatmap draws the viewer's
// local days ("local"). Mixing them would put the first hours of the 1st in the previous month.
import { shiftDayKey, zonedDayKey } from "./intl.ts";

export type RangePresetId = "this_month" | "last_month" | "last_30";
export type RangeZone = "utc" | "local";

export const RANGE_PRESETS: RangePresetId[] = ["this_month", "last_month", "last_30"];

const pad2 = (n: number) => String(n).padStart(2, "0");

/** Today's date key in the chosen calendar. */
export function todayKey(now: Date, zone: RangeZone): string {
  return zone === "utc" ? now.toISOString().slice(0, 10) : zonedDayKey(now);
}

/** Inclusive [from, to] of a preset. "this month" ends today (the month is not over); "last
 *  month" is the whole previous calendar month, January stepping back to the previous December. */
export function presetRange(id: RangePresetId, now: Date, zone: RangeZone): { from: string; to: string } {
  const today = todayKey(now, zone);
  const y = +today.slice(0, 4);
  const m = +today.slice(5, 7); // 1-12
  const first = `${String(y).padStart(4, "0")}-${pad2(m)}-01`;
  switch (id) {
    case "this_month":
      return { from: first, to: today };
    case "last_month":
      // The day before the 1st is the last day of the previous month, whatever its length.
      return { from: shiftDayKey(first, -1).slice(0, 8) + "01", to: shiftDayKey(first, -1) };
    case "last_30":
      return { from: shiftDayKey(today, -29), to: today };
  }
}

/** The preset whose range equals from/to, or "custom". */
export function matchPreset(from: string, to: string, now: Date, zone: RangeZone): RangePresetId | "custom" {
  for (const id of RANGE_PRESETS) {
    const r = presetRange(id, now, zone);
    if (r.from === from && r.to === to) return id;
  }
  return "custom";
}
