// Console の自前 i18n ランタイム（docs/log/28-i18n.md / ADR 0016）。外部ライブラリを入れず、
// ロケールの真実源は lib/settings.ts（settings.locale）に置く。ここは「現ロケール＋カタログ＋t()」
// だけを持ち、settings.ts が applyLocale() 経由で setLocale() を push する（この向きだけ。i18n は
// settings.ts を import しない＝循環回避）。t() は React・非React どちらからも呼べる（errText /
// notifications wording / 読み上げが非React）。
import { useSyncExternalStore } from "react";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

// ja を正本に全キーを型化。t() はこの union のみ受け付け、未知キーは tMaybe() を使う。
export type MsgKey = keyof typeof ja;

// 複数形キーの「基底」＝ `_other` バリアントを持つキーから接尾辞を除いた名前。tCount() は
// この基底のみ受け付け、`${base}_${category}`（Intl.PluralRules で選ぶ one/other/…）を引く。
// ja/en とも `_other` は必須（`_one` 等も両ロケールに置く＝完全性ガードで tsc が担保）。
type PluralBaseOf<K> = K extends `${infer B}_other` ? B : never;
export type PluralKey = PluralBaseOf<MsgKey>;

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

// Intl.PluralRules は現ロケール単位でメモ化（切替時は別ロケールで作り直す）。
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

// tCount — 複数形。count に応じ `${base}_${category}`（en は one/other、ja は常に other）を引く。
// count は自動で vars に混ぜるので、カタログ値では {count} をそのまま使える。カテゴリ欠落時は
// `_other` へフォールバック（日本語は単一形＝常に _other で足りる）。
export function tCount(base: PluralKey, count: number, vars?: Record<string, string | number>): string {
  const category = pluralRules().select(count);
  const s = lookup(`${base}_${category}`) ?? lookup(`${base}_other`) ?? base;
  return interpolate(s, { count, ...vars });
}

// tLocales — あるキーの「全ロケール分の訳」を配列で返す（重複除去）。現ロケールに依存せず
// ja/en 双方の文言を得るための検索インデックス用（コマンドパレットが日本語でも英語でも
// マッチできるように使う）。キーが無ければ空配列。
export function tLocales(key: string, vars?: Record<string, string | number>): string[] {
  const out = new Set<string>();
  for (const loc of SUPPORTED_LOCALES) {
    const s = CATALOGS[loc][key];
    if (s !== undefined) out.add(interpolate(s, vars));
  }
  return [...out];
}

// useT — ロケール変更で再レンダーさせたい React 側で使う。返す t は安定参照。
export function useT(): typeof t {
  useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  return t;
}

// useLocale — 現ロケール文字列そのものを購読する（変更で再レンダー）。native <input type="date">
// の lang など、翻訳ではなくロケール値が要る箇所で使う。
export function useLocale(): string {
  return useSyncExternalStore(subscribeLocale, getLocale, getLocale);
}
