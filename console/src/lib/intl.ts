// Locale-aware relative-time, date-time and number formatters (docs/log/28 P4 / ADR 0016). The
// hand-written duplicates (relTime x3, date-time x4) are folded into Intl.* and rendered in the
// current locale (getLocale from i18n); JS ships Intl data for both ja and en. Intl objects are
// memoized per locale x options, so a locale switch simply builds them under a new key. Nothing
// here depends on React, so non-React callers (WsBar and friends) can use it directly.
import { bumpFormatRev, getLocale, t } from "./i18n/index.ts";

// Region format and time zone (#2185). Both are member settings pushed in by applyLocale()
// (lib/settings/appearance.ts), like the display language; "auto" is a stored value of its own and
// is resolved here on every call, so a member who travels follows the browser's current zone
// instead of a copy frozen when the setting was saved.
export const FORMAT_AUTO = "auto";
let regionPref = FORMAT_AUTO;
let zonePref = FORMAT_AUTO;

// isValidTimeZone — true when Intl accepts name as an IANA zone. Never throws.
export function isValidTimeZone(name: unknown): name is string {
  if (typeof name !== "string" || name === "") return false;
  try {
    new Intl.DateTimeFormat("en", { timeZone: name });
    return true;
  } catch {
    return false;
  }
}

function isValidRegion(tag: unknown): tag is string {
  if (typeof tag !== "string" || tag === "") return false;
  try {
    return Intl.DateTimeFormat.supportedLocalesOf(tag).length > 0;
  } catch {
    return false;
  }
}

export function setFormatPrefs(region: string, timeZone: string): void {
  if (region === regionPref && timeZone === zonePref) return;
  regionPref = region;
  zonePref = timeZone;
  bumpFormatRev();
}

// formatLocale — the locale dates and numbers are written in: the chosen region format, or the
// display language when it is "auto" or unusable.
export function formatLocale(): string {
  return isValidRegion(regionPref) ? regionPref : getLocale();
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// selectedTimeZone — the explicit zone the member chose, or undefined for "follow the browser"
// (also what a stored name Intl does not know becomes; it must never throw).
export function selectedTimeZone(): string | undefined {
  return isValidTimeZone(zonePref) ? zonePref : undefined;
}

// effectiveTimeZone — the IANA zone every timestamp is shown in right now.
export function effectiveTimeZone(): string {
  return selectedTimeZone() ?? browserTimeZone();
}

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
  const loc = formatLocale();
  // Always an explicit zone: a formatter built without one freezes the browser's zone at
  // construction, and the cache would keep it after the member travels.
  const zone = effectiveTimeZone();
  const key = loc + ":" + zone + ":" + JSON.stringify(opts);
  let f = dtfCache.get(key);
  if (!f) {
    f = new Intl.DateTimeFormat(loc, { timeZone: zone, ...opts });
    dtfCache.set(key, f);
  }
  return f;
}

const nfCache = new Map<string, Intl.NumberFormat>();
function nf(opts?: Intl.NumberFormatOptions): Intl.NumberFormat {
  const loc = formatLocale();
  const key = loc + ":" + (opts ? JSON.stringify(opts) : "");
  let f = nfCache.get(key);
  if (!f) {
    f = new Intl.NumberFormat(loc, opts);
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
// toLocaleString, except that it uses the region format (default: the app language) rather than the browser's.
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
