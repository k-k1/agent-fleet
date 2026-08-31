// PDF ペインの「どのページを、どの大きさで、いつ描くか」の算術（docs/82 §82.5）。
//
// DOM に触れない純関数だけをここに置く。描画は canvas とスクロール位置に依存する
// ので、テストできる形はこの層しかない —— 実際、取り違えると出る事故（拡大したのに
// 前の倍率の canvas が残る、画面外のページまで一斉に描いてメモリを食う）は、すべて
// この計算の間違いとして再現できる。

/** 1 ページの素の大きさ（pdf.js の scale=1 ビューポート = CSS px）。 */
export interface PageSize {
  w: number;
  h: number;
}

/** ページを縦に積んだときの配置。tops[i] はスクロール容器内の上端 y。 */
export interface PageLayout {
  tops: number[];
  sizes: PageSize[];
  total: number;
}

/** ページの間隔と外周の余白（CSS の .pdfview-doc と一致させること）。 */
export const PAGE_GAP = 12;

/** 表示倍率の段。1 が「幅に合わせる」。 */
export const ZOOM_STEPS: number[] = [0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];

/** いま拡大している位置から次の段へ。端では動かない。 */
export function stepZoom(zoom: number, dir: 1 | -1): number {
  const steps = ZOOM_STEPS;
  if (dir > 0) return steps.find((s) => s > zoom + 1e-6) ?? steps[steps.length - 1];
  let out = steps[0];
  for (const s of steps) if (s < zoom - 1e-6) out = s;
  return out;
}

/** 容器の幅にいちばん広いページを合わせる倍率。
 *
 *  合わせる先を「いちばん広いページ」にするのは、横向きページが 1 枚混ざった文書で
 *  ページごとに倍率が変わると、読んでいる最中に段組みが飛ぶため。幅が取れない
 *  （まだ measure 前の）ときは 1 を返して、描画を次のレイアウトまで遅らせる。 */
export function fitScale(sizes: PageSize[], containerWidth: number, padding: number): number {
  const usable = containerWidth - padding * 2;
  if (!(usable > 0)) return 1;
  const widest = sizes.reduce((m, s) => Math.max(m, s.w), 0);
  if (!(widest > 0)) return 1;
  return usable / widest;
}

/** 倍率を当てたときの各ページの位置と全体の高さ。 */
export function layoutPages(sizes: PageSize[], scale: number, gap = PAGE_GAP): PageLayout {
  const tops: number[] = [];
  const scaled: PageSize[] = [];
  let y = 0;
  for (const s of sizes) {
    const w = Math.max(1, Math.round(s.w * scale));
    const h = Math.max(1, Math.round(s.h * scale));
    tops.push(y);
    scaled.push({ w, h });
    y += h + gap;
  }
  return { tops, sizes: scaled, total: Math.max(0, y - gap) };
}

/** いま描くべきページ番号（0 始まり）の範囲。overscan は画面の何倍を先読みするか。
 *
 *  返すのは半開区間 [start, end)。ページが 0 枚なら start === end === 0。 */
export function visibleRange(
  layout: PageLayout,
  scrollTop: number,
  viewportHeight: number,
  overscan = 0.5,
): { start: number; end: number } {
  const n = layout.tops.length;
  if (n === 0 || viewportHeight <= 0) return { start: 0, end: 0 };
  const margin = viewportHeight * overscan;
  const top = scrollTop - margin;
  const bottom = scrollTop + viewportHeight + margin;
  let start = n;
  let end = 0;
  for (let i = 0; i < n; i++) {
    const a = layout.tops[i];
    const b = a + layout.sizes[i].h;
    if (b < top || a > bottom) continue;
    if (i < start) start = i;
    if (i + 1 > end) end = i + 1;
  }
  return start > end ? { start: 0, end: 0 } : { start, end };
}

/** 情報バーに出す「いま見ているページ」（1 始まり）。
 *
 *  画面の中央にある方のページを現在ページとする。上端で決めると、ページの境目が
 *  画面上端に来た瞬間に、ほとんど見えていない次ページへ番号が飛ぶ。 */
export function currentPage(layout: PageLayout, scrollTop: number, viewportHeight: number): number {
  const n = layout.tops.length;
  if (n === 0) return 1;
  const probe = scrollTop + viewportHeight / 2;
  for (let i = n - 1; i >= 0; i--) if (layout.tops[i] <= probe) return i + 1;
  return 1;
}

/** 倍率を変える前に覚えておく読み位置（ページ番号＋そのページ内の割合）。 */
export interface ScrollAnchor {
  page: number;
  fraction: number;
}

/** いまの読み位置を、倍率に依らない形（ページ＋割合）で取り出す。 */
export function anchorOf(layout: PageLayout, scrollTop: number): ScrollAnchor {
  const n = layout.tops.length;
  if (n === 0) return { page: 0, fraction: 0 };
  let page = 0;
  for (let i = n - 1; i >= 0; i--) {
    if (layout.tops[i] <= scrollTop) {
      page = i;
      break;
    }
  }
  const h = layout.sizes[page].h + PAGE_GAP;
  return { page, fraction: h > 0 ? (scrollTop - layout.tops[page]) / h : 0 };
}

/** 覚えた読み位置を、新しい倍率のレイアウトでのスクロール位置へ戻す。 */
export function scrollTopForAnchor(layout: PageLayout, anchor: ScrollAnchor): number {
  const n = layout.tops.length;
  if (n === 0) return 0;
  const page = Math.min(Math.max(0, anchor.page), n - 1);
  const h = layout.sizes[page].h + PAGE_GAP;
  return Math.max(0, Math.round(layout.tops[page] + anchor.fraction * h));
}

/** canvas の実ピクセル数。画面の密度に合わせつつ、1 ページの総画素で頭打ちにする。
 *
 *  Retina で 4 倍の canvas を何枚も持つと、長い文書ではタブごと落ちる。上限は
 *  「A4 を 300dpi 相当で持てる」程度に置き、超える分は解像度を落として面積を守る。 */
export const MAX_CANVAS_PIXELS = 8_000_000;

export function canvasPixelRatio(cssW: number, cssH: number, dpr: number): number {
  const ratio = Math.min(Math.max(1, dpr), 3);
  const area = cssW * cssH * ratio * ratio;
  if (area <= MAX_CANVAS_PIXELS || cssW <= 0 || cssH <= 0) return ratio;
  return Math.max(0.5, Math.sqrt(MAX_CANVAS_PIXELS / (cssW * cssH)));
}
