import { zonedDayKey } from "../../../lib/intl.ts";

/** YYYY-MM-DD in the member's time zone, or "" for a missing or unreadable value. */
export function fmtDate(s: string | undefined): string {
  if (!s) return "";
  return zonedDayKey(s);
}
