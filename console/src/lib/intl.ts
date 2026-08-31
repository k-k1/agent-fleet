// ロケール対応の相対時刻・日時・数値フォーマッタ（docs/log/28 P4 / ADR 0016）。手書きで重複していた
// relTime(×3) と日時(×4) を Intl.* に一本化し、現ロケール（i18n の getLocale）で描画する。JS の
// Intl は ja/en とも標準搭載。Intl オブジェクトはロケール×オプションでメモ化する（切替時は
// getLocale() が変わり別キーで作り直す）。ロケール非依存の集約点なので、非React（WsBar 等）からも
// そのまま呼べる。
import { getLocale, t } from "./i18n/index.ts";

const rtfCache = new Map<string, Intl.RelativeTimeFormat>();
function rtf(): Intl.RelativeTimeFormat {
  const loc = getLocale();
  let f = rtfCache.get(loc);
  if (!f) {
    // numeric:"always" ＝「昨日/明日」等の語置換を避け、手書き版に近い "N 日前"/"N days ago" を出す。
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

// 入力を epoch ミリ秒へ。number は「ミリ秒」とみなす（unix 秒の呼び出し側は ×1000 して渡す）。
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

// relTime — 過去/未来の相対時刻。<60秒 は "たった今"/"just now"（common.just_now）、以降は
// Intl.RelativeTimeFormat の梯子（year→minute）。負の差=過去。無効値・null/空は "" を返す。
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

// 既定の日時フォーマット ＝ "M/D HH:MM"(24h) 相当。hourCycle:"h23" で 00〜23 に固定（hour12:false の
// 深夜 24:00 表記ゆれを避ける）。粒度を変えたい呼び出しは opts を渡す（TIME_HM / DATETIME_FULL）。
const DATETIME_MD: Intl.DateTimeFormatOptions = {
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};
// 時刻のみ "HH:MM"。
export const TIME_HM: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
// 年月日＋時分秒（旧 Date.toLocaleString 相当）。
export const DATETIME_FULL: Intl.DateTimeFormatOptions = {
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
};

// fmtDateTime — 絶対日時。無効値は文字列入力ならそのまま、それ以外は "" を返す（旧実装の挙動）。
export function fmtDateTime(when: string | number | Date, opts: Intl.DateTimeFormatOptions = DATETIME_MD): string {
  const ms = toMs(when);
  if (isNaN(ms)) return typeof when === "string" ? when : "";
  return dtf(opts).format(ms);
}

// fmtNum — ロケール対応の数値（桁区切り等）。既定は素の toLocaleString 相当だがアプリロケール基準。
export function fmtNum(n: number, opts?: Intl.NumberFormatOptions): string {
  return nf(opts).format(n);
}

// compareText — 名前・キーのソート用のロケール対応文字列比較。素の a.localeCompare(b) は暗黙の
// ブラウザ既定ロケールに依存するため、明示的にアプリロケール（getLocale）で比較する。
// numeric:true で "repo2 < repo10"（数字を数値として）、sensitivity:"base" で大文字小文字/
// アクセント差を無視した安定した並び。日付文字列（ISO）にも順序は変わらず安全に使える。
export function compareText(a: string, b: string): number {
  return a.localeCompare(b, getLocale(), { numeric: true, sensitivity: "base" });
}
