// どのペインの文字サイズを動かすのか、を決める純ロジック。
//
// 文字サイズは「面ごとに 1 本のグローバル設定」で持っている（設定 › 表示のステッパーと、
// 朗読ビューの ＋/− ボタンが動かしているのと同じ値）。キーボードの拡大/縮小はそれを踏襲し、
// **アクティブなペインが属する面の設定**を上下させる —— 新しい永続状態を増やさないので、
// 設定画面の表示・クロスデバイス同期・既定へのリセットが全部そのまま効く。
//
// store も DOM も import しない（lib の原則）。layout の型だけ type-only で借りる。
import type { PaneContent } from "../layout/types.ts";
import { imageFormat } from "./filemeta.ts";

/** 文字サイズを持つ 4 つの面（lib/settings.ts の同名キー）。 */
export type FontSetting = "termSize" | "viewerSize" | "chatSize" | "readerSize";

// 設定 › 表示のステッパーと同じ範囲。両方がこの定数を使うのでズレない。
export const FONT_MIN = 9;
export const FONT_MAX = 28;

/** このペインの文字サイズを支配している設定キー。null＝文字組みを持たない面
 *  （ブラウザ・画像）で、呼び出し側はキーを握らずに端末へ流す。 */
export function fontSettingFor(content: PaneContent | null | undefined): FontSetting | null {
  if (!content) return null; // 空セル
  switch (content.kind) {
    // 端末ペインは chat=true のときミラー（会話）を描くので、面としては chat 側。
    case "terminal":
      return content.chat ? "chatSize" : "termSize";
    case "chat":
    case "sharedSession":
      return "chatSize";
    case "read":
      return "readerSize";
    // 画像だけは文字を持たない。drawio は図とソースを行き来できるので対象に残す。
    case "file":
      return imageFormat(content.filePath) ? null : "viewerSize";
    case "diff":
    case "wtdiff":
    case "scm":
    case "changes":
    case "commit":
    case "doc":
      return "viewerSize";
    // browser / browserAttach: ページ側の拡大縮小であってこちらの設定ではない。
    default:
      return null;
  }
}

/** 1 段動かした値（範囲外へは出ない）。 */
export const stepFontSize = (current: number, delta: number): number =>
  Math.min(FONT_MAX, Math.max(FONT_MIN, current + delta));
