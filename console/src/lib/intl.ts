// Locale-aware relative-time, date-time and number formatters (docs/log/28 P4 / ADR 0016). The
// hand-written duplicates (relTime x3, date-time x4) are folded into Intl.* and rendered in the
// current locale (getLocale from i18n); JS ships Intl data for both ja and en. Intl objects are
// memoized per locale x options, so a locale switch simply builds them under a new key. Nothing
// here depends on React, so non-React callers (WsBar and friends) can use it directly.
import { getLocale, t } from "./i18n/index.ts";

const rtfCache = new Map<string, Intl.RelativeTimeFormat>();
function rtf(): Intl.RelativeTimeFormat {
  const loc = getLocale();
  let f = rtfCache.get(loc);
  if (!f) {
    // numeric:"always" avoids word substitutions like "yesterday"/"tomorrow" and keeps the
    // "N days ago" shape the hand-written version had.
    f = new Intl.RelativeTimeFormat(loc, { numeric: "always" });
    rtfCache.set(loc, f);
  }
  return f;
}

const dtfCache = new Map<string, Intl.DateTimeFormat>();
function dtf(opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = getLocale() + ":" + JSON.stringify(opts);
  let f = dtfCache.get(key);
  if (!f) {
    f = new Intl.DateTimeFormat(getLocale(), opts);
    dtfCache.set(key, f);
  }
  return f;
}

const nfCache = new Map<string, Intl.NumberFormat>();
function nf(opts?: Intl.NumberFormatOptions): Intl.NumberFormat {
  const key = getLocale() + ":" + (opts ? JSON.stringify(opts) : "");
  let f = nfCache.get(key);
  if (!f) {
    f = new Intl.NumberFormat(getLocale(), opts);
    nfCache.set(key, f);
  }
  return f;
}

// Normalize the input to epoch milliseconds. A number is taken as milliseconds; callers holding
// unix seconds must multiply by 1000 themselves.
function toMs(when: string | number | Date): number {
  if (when instanceof Date) return when.getTime();
  if (typeof when === "number") return when;
  return new Date(when).getTime();
}

const REL_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 31536000],
  ["month", 2592000],
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
];

// relTime — relative time, past or future. Under 60 seconds renders common.just_now; beyond that
// it walks the Intl.RelativeTimeFormat ladder (year down to minute). A negative difference is the
// past. Invalid values, null and "" all return "".
export function relTime(when: string | number | Date | null | undefined, now: number = Date.now()): string {
  if (when == null || when === "") return "";
  const ms = toMs(when);
  if (isNaN(ms)) return "";
  const diffSec = Math.round((ms - now) / 1000);
  if (Math.abs(diffSec) < 60) return t("common.just_now");
  for (const [unit, secs] of REL_UNITS) {
    if (Math.abs(diffSec) >= secs) return rtf().format(Math.round(diffSec / secs), unit);
  }
  return t("common.just_now");
}

// Default date-time format, equivalent to "M/D HH:MM" (24h). hourCycle:"h23" pins the hour to
// 00-23; hour12:false lets midnight render inconsistently as 24:00. Callers wanting another
// granularity pass opts (TIME_HM / DATETIME_FULL).
const DATETIME_MD: Intl.DateTimeFormatOptions = {
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};
// Time only, "HH:MM".
export const TIME_HM: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
// Full date plus hours, minutes and seconds (what Date.toLocaleString used to give).
export const DATETIME_FULL: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
};

// fmtDateTime — absolute date-time. An invalid value is echoed back when the input was a string,
// otherwise "" (the previous implementation's behaviour).
export function fmtDateTime(when: string | number | Date, opts: Intl.DateTimeFormatOptions = DATETIME_MD): string {
  const ms = toMs(when);
  if (isNaN(ms)) return typeof when === "string" ? when : "";
  return dtf(opts).format(ms);
}

// fmtNum — locale-aware numbers (grouping separators and so on). The default matches plain
// toLocaleString, except that it uses the app locale rather than the browser's.
export function fmtNum(n: number, opts?: Intl.NumberFormatOptions): string {
  return nf(opts).format(n);
}

// compareText — locale-aware string comparison for sorting names and keys. A bare a.localeCompare(b)
// depends on the browser's implicit default locale, so compare in the app locale (getLocale)
// explicitly. numeric:true gives "repo2 < repo10" (digits compared as numbers) and
// sensitivity:"base" ignores case and accent differences for a stable order. Safe on ISO date
// strings too — it does not change their ordering.
export function compareText(a: string, b: string): number {
  return a.localeCompare(b, getLocale(), { numeric: true, sensitivity: "base" });
}
