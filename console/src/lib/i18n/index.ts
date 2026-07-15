// Console の自前 i18n ランタイム（docs/28-i18n.md / ADR 0016）。外部ライブラリを入れず、
// ロケールの真実源は lib/settings.ts（settings.locale）に置く。ここは「現ロケール＋カタログ＋t()」
// だけを持ち、settings.ts が applyLocale() 経由で setLocale() を push する（この向きだけ。i18n は
// settings.ts を import しない＝循環回避）。t() は React・非React どちらからも呼べる（errText /
// notifications wording / 読み上げが非React）。
import { useSyncExternalStore } from "react";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

// ja を正本に全キーを型化。t() はこの union のみ受け付け、未知キーは tMaybe() を使う。
export type MsgKey = keyof typeof ja;

const CATALOGS: Record<string, Record<string, string>> = { ja, en };

// 対応ロケール（カタログを持つもの）。settings 既定値の解決やピッカーの妥当性判定に使う。
export const SUPPORTED_LOCALES = Object.keys(CATALOGS);
export const DEFAULT_LOCALE = "ja";

let currentLocale = DEFAULT_LOCALE;
const listeners = new Set<() => void>();

// setLocale — settings.ts の applyLocale() から push される。未対応ロケールは無視して既定を維持。
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

// {name} 形式のプレースホルダを vars で置換。該当キーが無ければそのまま残す（デバッグ用に可視）。
function interpolate(s: string, vars?: Record<string, string | number>): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

function lookup(key: string): string | undefined {
  const cat = CATALOGS[currentLocale] || CATALOGS[DEFAULT_LOCALE];
  // 現ロケールに欠けていれば ja（正本）へフォールバック。型ガードで通常は起きない。
  return cat[key] ?? CATALOGS[DEFAULT_LOCALE][key];
}

// t — 型付きキー版。カタログに必ず存在する前提（欠落は tsc が検出）。
export function t(key: MsgKey, vars?: Record<string, string | number>): string {
  return interpolate(lookup(key) ?? key, vars);
}

// tMaybe — 実行時に決まる動的キー（例: "err." + backend code）用。無ければ undefined。
export function tMaybe(key: string, vars?: Record<string, string | number>): string | undefined {
  const s = lookup(key);
  return s === undefined ? undefined : interpolate(s, vars);
}

// useT — ロケール変更で再レンダーさせたい React 側で使う。返す t は安定参照。
export function useT(): typeof t {
  useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  return t;
}
