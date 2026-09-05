// The Console's own i18n runtime (docs/log/28-i18n.md / ADR 0016). No external library; the source
// of truth for the locale is lib/settings.ts (settings.locale). This module holds only the current
// locale, the catalogs and t(); settings.ts pushes setLocale() through applyLocale(). That direction
// only — i18n must not import settings.ts, or the two form a cycle. t() is callable from React and
// non-React code alike (errText, notifications wording and text-to-speech are non-React).
import { useSyncExternalStore } from "react";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

// Every key is typed from ja, the master catalog. t() accepts only this union; unknown keys go
// through tMaybe().
export type MsgKey = keyof typeof ja;

// The "base" of a plural key: a key with an `_other` variant, minus that suffix. tCount() accepts
// only such a base and looks up `${base}_${category}` (one/other/… chosen by Intl.PluralRules).
// `_other` is mandatory in both ja and en, and any `_one` etc. must exist in both locales too —
// the completeness guard makes tsc enforce that.
type PluralBaseOf<K> = K extends `${infer B}_other` ? B : never;
export type PluralKey = PluralBaseOf<MsgKey>;

const CATALOGS: Record<string, Record<string, string>> = { ja, en };

// Supported locales (the ones that have a catalog). Used to resolve the settings default and to
// validate the picker's value.
export const SUPPORTED_LOCALES = Object.keys(CATALOGS);
export const DEFAULT_LOCALE = "ja";

let currentLocale = DEFAULT_LOCALE;
const listeners = new Set<() => void>();

// setLocale — pushed in by applyLocale() in settings.ts. An unsupported locale is ignored and the
// default is kept.
export function setLocale(locale: string): void {
  const next = CATALOGS[locale] ? locale : DEFAULT_LOCALE;
  if (next === currentLocale) return;
  currentLocale = next;
  listeners.forEach((fn) => fn());
}

export function getLocale(): string {
  return currentLocale;
}

export function subscribeLocale(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

// Replace {name} placeholders from vars. A placeholder with no matching key is left in place so it
// stays visible while debugging.
function interpolate(s: string, vars?: Record<string, string | number>): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

function lookup(key: string): string | undefined {
  const cat = CATALOGS[currentLocale] || CATALOGS[DEFAULT_LOCALE];
  // Fall back to ja (the master catalog) when the current locale lacks the key. The type guard
  // normally makes this impossible.
  return cat[key] ?? CATALOGS[DEFAULT_LOCALE][key];
}

// t — typed-key version. The key is assumed to exist in the catalog; tsc catches a missing one.
export function t(key: MsgKey, vars?: Record<string, string | number>): string {
  return interpolate(lookup(key) ?? key, vars);
}

// tMaybe — for keys computed at runtime (e.g. "err." + backend code). undefined when absent.
export function tMaybe(key: string, vars?: Record<string, string | number>): string | undefined {
  const s = lookup(key);
  return s === undefined ? undefined : interpolate(s, vars);
}

// Intl.PluralRules is memoized per locale; switching locale builds a new one under its own key.
const prCache = new Map<string, Intl.PluralRules>();
function pluralRules(): Intl.PluralRules {
  const loc = currentLocale;
  let p = prCache.get(loc);
  if (!p) {
    p = new Intl.PluralRules(loc);
    prCache.set(loc, p);
  }
  return p;
}

// tCount — plurals. Looks up `${base}_${category}` for the count (en: one/other; ja: always other).
// count is merged into vars automatically, so catalog values can use {count} directly. A missing
// category falls back to `_other`, which is all Japanese ever needs since it has one form.
export function tCount(base: PluralKey, count: number, vars?: Record<string, string | number>): string {
  const category = pluralRules().select(count);
  const s = lookup(`${base}_${category}`) ?? lookup(`${base}_other`) ?? base;
  return interpolate(s, { count, ...vars });
}

// tLocales — every locale's rendering of one key, deduplicated. For search indexes that need both
// the ja and en wording regardless of the current locale, so the command palette matches whichever
// language the user types. Empty array when the key is unknown.
export function tLocales(key: string, vars?: Record<string, string | number>): string[] {
  const out = new Set<string>();
  for (const loc of SUPPORTED_LOCALES) {
    const s = CATALOGS[loc][key];
    if (s !== undefined) out.add(interpolate(s, vars));
  }
  return [...out];
}

// useT — for React code that must re-render on a locale change. The returned t is a stable ref.
export function useT(): typeof t {
  useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  return t;
}

// useLocale — subscribe to the locale string itself (re-renders on change). For places that need
// the locale value rather than a translation, such as the lang of a native <input type="date">.
export function useLocale(): string {
  return useSyncExternalStore(subscribeLocale, getLocale, getLocale);
}
