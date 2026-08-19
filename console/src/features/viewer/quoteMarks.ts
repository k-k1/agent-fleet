// 描画済み Markdown の中の「引用箇所」を数え、復元し、ハイライトを被せるための道具。
//
// なぜオフセットではなく「引用文字列 + 出現番号」なのか: MarkdownView は Markdown を
// innerHTML で描くので、元ソースの文字位置は手元に残らない。逆に描画後のテキストは安定して
// いるので、そこでの n 番目の一致をアンカーにする（W3C Web Annotation の TextQuoteSelector
// と同じ考え方）。本文が改訂されて一致しなくなったときは、ハイライトが付かないだけで、
// 別の箇所へ誤って付くことはない。
//
// 純粋関数（occurrenceOf / indexOfNth）と DOM 操作を分けてあるので、数え方の回帰は
// node プロジェクトのテストで固定できる。

/** text の [0, start) に quote が何回現れるか = start から始まる一致の出現番号。 */
export function occurrenceOf(text: string, quote: string, start: number): number {
  if (!quote) return 0;
  let n = 0;
  for (let i = text.indexOf(quote); i >= 0 && i < start; i = text.indexOf(quote, i + 1)) n++;
  return n;
}

/** nth 番目（0 始まり）の一致の開始位置。見つからなければ -1。 */
export function indexOfNth(text: string, quote: string, nth: number): number {
  if (!quote) return -1;
  let i = text.indexOf(quote);
  for (let n = 0; i >= 0 && n < nth; n++) i = text.indexOf(quote, i + 1);
  return i;
}

interface TextSpan {
  node: Text;
  start: number; // root 全体のテキスト中での開始位置
}

// テキストノードを文書順に集め、各ノードの開始オフセットを付ける。ハイライト用の <mark> は
// 数える対象から外さない（既存マークの中の文字も本文の一部）。
function textSpans(root: HTMLElement): { spans: TextSpan[]; text: string } {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const spans: TextSpan[] = [];
  let text = "";
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const node = n as Text;
    spans.push({ node, start: text.length });
    text += node.nodeValue || "";
  }
  return { spans, text };
}

/** root 内の (node, offset) が root のテキスト全体で何文字目か。 */
function offsetIn(spans: TextSpan[], node: Node, offset: number): number {
  for (const s of spans) {
    if (s.node === node) return s.start + offset;
  }
  return -1;
}

export interface QuoteAnchor {
  quote: string;
  nth: number;
}

/** いま root 内で確定している選択を引用アンカーにする。選択が無い／root の外なら null。 */
export function selectionAnchor(root: HTMLElement): (QuoteAnchor & { rect: DOMRect }) | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const range = sel.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const quote = sel.toString().trim();
  if (!quote) return null;
  const { spans, text } = textSpans(root);
  const start = offsetIn(spans, range.startContainer, range.startOffset);
  // 選択の先頭が空白だったぶんズレるので、実際の一致位置を start 以降で採り直す。
  const at = start < 0 ? text.indexOf(quote) : text.indexOf(quote, Math.max(0, start - quote.length));
  if (at < 0) return null;
  return { quote, nth: occurrenceOf(text, quote, at), rect: range.getBoundingClientRect() };
}

/** 被せたハイライトを剥がす（テキストノードは元通りに繋ぎ直す）。 */
export function clearMarks(root: HTMLElement, selector: string): void {
  const marks = [...root.querySelectorAll<HTMLElement>(selector)];
  for (const m of marks) {
    const parent = m.parentNode;
    if (!parent) continue;
    while (m.firstChild) parent.insertBefore(m.firstChild, m);
    parent.removeChild(m);
  }
  if (marks.length) root.normalize(); // 分割された text ノードを戻す（次の数えがズレないように）
}

/** applyQuoteMarks が付けたハイライトを剥がす。 */
export function clearQuoteMarks(root: HTMLElement): void {
  clearMarks(root, "mark.quote-mark");
}

/** 被せる 1 件: どこを（quote/nth）、どんな見た目で（className/dataset）。 */
export interface PaintedMark extends QuoteAnchor {
  className: string;
  dataset?: Record<string, string>;
}

/**
 * アンカーの箇所を <mark> で囲む。返り値は「何番目のアンカーが実際に見つかったか」—
 * 改訂で消えた指摘をカード側で灰色にできる。
 *
 * 1つの引用が複数要素にまたがることがある（段落をまたぐ選択、太字の途中など）ので、
 * Range.surroundContents は使わず、重なるテキストノードごとに切って包む。DOM を触りながら
 * 走査すると位置が狂うので、先に対象を集めてから書き換える（MarkdownView の renderEmoji と
 * 同じ作法）。
 *
 * selector は「前回この関数が付けたもの」を剥がすためのもので、面ごとに別の class を使う
 * （プランコメントの引用と転写のマーカーが互いを消し合わないように）。
 */
export function applyPaintedMarks(root: HTMLElement, marks: PaintedMark[], selector: string): boolean[] {
  clearMarks(root, selector);
  const { spans, text } = textSpans(root);
  const found = marks.map(() => false);
  // 後ろから処理すると、同じテキストノードを2回切っても先に確定した位置がズレない。
  const targets = marks
    .map((m, i) => ({ i, at: indexOfNth(text, m.quote, m.nth), len: m.quote.length }))
    .filter((x) => x.at >= 0)
    .sort((a, b) => b.at - a.at);

  for (const target of targets) {
    const end = target.at + target.len;
    for (const s of spans) {
      const node = s.node;
      const nodeEnd = s.start + (node.nodeValue || "").length;
      if (nodeEnd <= target.at || s.start >= end) continue; // 重ならない
      if (!node.parentNode) continue; // 直前の切り出しで置き換わっている
      const from = Math.max(0, target.at - s.start);
      const to = Math.min((node.nodeValue || "").length, end - s.start);
      const mark = document.createElement("mark");
      mark.className = marks[target.i].className;
      for (const [k, v] of Object.entries(marks[target.i].dataset || {})) mark.dataset[k] = v;
      const range = document.createRange();
      range.setStart(node, from);
      range.setEnd(node, to);
      try {
        range.surroundContents(mark); // 単一テキストノード内なので必ず成立する
        found[target.i] = true;
      } catch {
        /* 想定外の構造: そのぶんのハイライトは諦める（本文は壊さない） */
      }
    }
  }
  return found;
}

/** プランコメントの引用ハイライト（番号バッジ付き）。 */
export function applyQuoteMarks(root: HTMLElement, anchors: QuoteAnchor[]): boolean[] {
  return applyPaintedMarks(
    root,
    anchors.map((a, i) => ({ ...a, className: "quote-mark", dataset: { n: String(i + 1) } })),
    "mark.quote-mark",
  );
}
