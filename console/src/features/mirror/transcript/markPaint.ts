// transcript/markPaint — 印を実際の DOM へ被せる（docs/69 / ADR 0050）。
//
// 切り貼りの本体は features/viewer/quoteMarks.ts（プランコメントと共通）。ここは
// 「1ターンぶんをまとめて塗り直すか、何もしないか」の判断だけを持つ。
//
// ⚠️ 判断が要るのは、転写がポーリングのたびに再描画されるから。ターンごとに毎回塗り直すと、
// 400 ターン × 毎秒の DOM 書き換えになる。逆に「印が変わったときだけ」にすると、
// MarkdownView が本文を作り直した回（テーマ変更など）に印が消えたまま戻らない。
// そこで「印の内容」「本文の長さ」「いま実際に載っている印の数」の 3 つで判断する。

import { applyPaintedMarks } from "../../viewer/quoteMarks.ts";
import type { TranscriptMark } from "./marks.ts";

export const MARK_CLASS = "tmark";
const MARK_SELECTOR = "mark." + MARK_CLASS;

function classOf(m: TranscriptMark, slot: number): string {
  return `${MARK_CLASS} ${MARK_CLASS}-${m.color} ${MARK_CLASS}-a${slot}`;
}

/**
 * body 配下の各 [data-mark-root] を塗り直す。返り値は次回の判断に使う指紋で、
 * 呼び出し側は前回と同じなら何もしない。
 */
export function paintTurnMarks(
  body: HTMLElement,
  byRoot: Map<string, TranscriptMark[]>,
  authorSlot: (author: string | undefined) => number,
  prevSignature: string,
): string {
  const roots = [...body.querySelectorAll<HTMLElement>("[data-mark-root]")];
  if (!roots.length) return "";
  const work: Array<{ el: HTMLElement; list: TranscriptMark[] }> = [];
  let want = 0;
  let sig = "";
  for (const el of roots) {
    const list = byRoot.get(el.dataset.markRoot || "") || [];
    want += list.length;
    if (list.length) {
      sig += list.map((m) => m.id + ":" + m.color + ":" + m.nth).join(",") + ";";
      sig += (el.textContent || "").length + "|";
    }
    work.push({ el, list });
  }
  // 何も付いていないターンは、いま何も載っていないなら触らない（大多数のターンがこれ）。
  const have = body.querySelectorAll(MARK_SELECTOR).length;
  if (want === 0 && have === 0) return "";
  // 本文が作り直されて印が消えた回は、指紋が同じでも塗り直す。
  if (sig === prevSignature && !(want > 0 && have === 0)) return sig;
  for (const { el, list } of work) {
    applyPaintedMarks(
      el,
      list.map((m) => ({
        quote: m.quote,
        nth: m.nth,
        className: classOf(m, authorSlot(m.author)),
        dataset: { markId: m.id },
      })),
      MARK_SELECTOR,
    );
  }
  return sig;
}
