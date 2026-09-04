// 描画済み Markdown の中の「引用箇所」を数え、復元し、ハイライトを被せるための道具。
//
// なぜオフセットではなく「引用文字列 + 出現番号」なのか: MarkdownView は Markdown を
// innerHTML で描くので、元ソースの文字位置は手元に残らない。逆に描画後のテキストは安定して
// いるので、そこでの n 番目の一致をアンカーにする（W3C Web Annotation の TextQuoteSelector
// と同じ考え方）。本文が改訂されて一致しなくなったときは、ハイライトが付かないだけで、
// 別の箇所へ誤って付くことはない。
//
// ⚠️ 数える土台は「正規化テキスト」でなければならない（2026-09-04 実測）。選択から採る
// `Selection.toString()` は**描画テキスト**——CSS の空白畳み込みが効き、`<br>`・段落・箇条書き
// の境界には元の DOM に無い改行が入る——のに対し、textContent の連結は**生テキスト**なので、
// 素の `indexOf` は次の選択で必ず外れ、ピッカーが無言で出なくなっていた:
//   - 段落内のソース改行をまたぐ  選択 "リソース 一覧"  ／ 生 "リソース\n一覧"
//   - `<br>` をまたぐ             選択 "one\ntwo"      ／ 生 "onetwo"（br は textContent に出ない）
//   - 段落・箇条書きの項目をまたぐ 選択 "…。\n\nsecond" ／ 生 は区切り無し
// そこで採取側も復元側も、空白の連なりを 1 個の空白へ畳んだテキストの上で数える
// （`normalizeQuote` / `flattenRoot`）。空白の「形」の違いだけを吸収するので、
// 「別の箇所へ誤って付かない」性質はそのまま。
//
// 純粋関数（occurrenceOf / indexOfNth / normalizeQuote）と DOM 操作を分けてあるので、
// 数え方の回帰は node プロジェクトのテストで固定できる。

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

/** 空白の連なりを 1 個の空白へ畳む。描画テキストと生テキストの「空白の形」の差を消すため。 */
export function normalizeQuote(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

/** 前後に改行が入って見える要素。ここを跨いだ選択には、生テキストに無い区切りが入る。 */
const BLOCK_TAGS = new Set([
  "ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "BR", "DD", "DETAILS", "DIV", "DL", "DT",
  "FIGCAPTION", "FIGURE", "FOOTER", "FORM", "H1", "H2", "H3", "H4", "H5", "H6", "HEADER",
  "HR", "LI", "MAIN", "NAV", "OL", "P", "PRE", "SECTION", "SUMMARY", "TABLE", "TD", "TH",
  "TR", "UL",
]);

interface TextSpan {
  node: Text;
  start: number; // root の生テキスト中での開始位置
}

interface FlatRoot {
  spans: TextSpan[];
  /** 空白を畳んだあとのテキスト。数えるのは必ずこちら。 */
  text: string;
  /** text[i] が生テキストの何文字目から来たか（非減少）。塗るときに生の位置へ戻す。 */
  rawAt: number[];
}

// テキストノードを文書順に集め、各ノードの開始オフセットを付けつつ、正規化テキストと
// 「正規化 → 生」の対応表を作る。ハイライト用の <mark> は数える対象から外さない
// （既存マークの中の文字も本文の一部）。
function flattenRoot(root: HTMLElement): FlatRoot {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
  const spans: TextSpan[] = [];
  const rawAt: number[] = [];
  let rawLen = 0;
  let text = "";
  let pending = false; // 直前に空白か要素境界があった
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.nodeType === Node.ELEMENT_NODE) {
      if (BLOCK_TAGS.has((n as Element).tagName)) pending = true;
      continue;
    }
    const node = n as Text;
    const value = node.nodeValue || "";
    spans.push({ node, start: rawLen });
    for (let i = 0; i < value.length; i++) {
      if (/\s/.test(value[i])) {
        pending = true;
        continue;
      }
      if (pending) {
        pending = false;
        // 先頭の空白は落とす（本文の頭に空白は無いので、数えが 1 文字ズレない）。
        if (text.length) {
          text += " ";
          rawAt.push(rawLen + i);
        }
      }
      text += value[i];
      rawAt.push(rawLen + i);
    }
    rawLen += value.length;
  }
  return { spans, text, rawAt };
}

/** root 内の (node, offset) が生テキスト全体で何文字目か。要素境界なら次のテキストノードの頭。 */
function offsetIn(spans: TextSpan[], node: Node, offset: number): number {
  for (const s of spans) {
    if (s.node === node) return s.start + offset;
  }
  // ダブルクリックや段落頭からの選択は startContainer が要素になる。その位置以降で最初に
  // 現れるテキストノードを開始位置とみなす（先頭からの検索に落とすと、同じ語の別の出現を
  // 拾ってしまう）。
  if (node.nodeType !== Node.ELEMENT_NODE) return -1;
  const probe = document.createRange();
  probe.setStart(node, Math.min(offset, node.childNodes.length));
  probe.collapse(true);
  for (const s of spans) {
    try {
      if (probe.comparePoint(s.node, 0) >= 0) return s.start;
    } catch {
      /* 比較できない位置は飛ばす */
    }
  }
  return -1;
}

/** 生テキストの位置 → 正規化テキストの位置（その位置以降で最初の文字）。 */
function normIndexOf(rawAt: number[], raw: number): number {
  let lo = 0;
  let hi = rawAt.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (rawAt[mid] < raw) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

export interface QuoteAnchor {
  quote: string;
  nth: number;
}

/**
 * range と「その選択の見た目の文字列」から引用アンカーを作る。
 *
 * selected を引数で受けるのは、`Selection.toString()`（描画テキスト）が jsdom では
 * 生テキストになり、この関数がまさに吸収している差をテストで作れなくなるから。
 */
export function anchorForRange(root: HTMLElement, range: Range, selected: string): QuoteAnchor | null {
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const quote = normalizeQuote(selected);
  if (!quote) return null;
  const { spans, text, rawAt } = flattenRoot(root);
  const rawStart = offsetIn(spans, range.startContainer, range.startOffset);
  // 選択の先頭が空白だったぶんズレるので、実際の一致位置を start 以降で採り直す。
  const from = rawStart < 0 ? 0 : Math.max(0, normIndexOf(rawAt, rawStart) - quote.length);
  const at = text.indexOf(quote, from);
  if (at < 0) return null;
  return { quote, nth: occurrenceOf(text, quote, at) };
}

/** いま root 内で確定している選択を引用アンカーにする。選択が無い／root の外なら null。 */
export function selectionAnchor(root: HTMLElement): (QuoteAnchor & { rect: DOMRect }) | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const range = sel.getRangeAt(0);
  const anchor = anchorForRange(root, range, sel.toString());
  if (!anchor) return null;
  return { ...anchor, rect: range.getBoundingClientRect() };
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
  const { spans, text, rawAt } = flattenRoot(root);
  const found = marks.map(() => false);
  // 保存済みの引用も畳んでから数える（改行を含む古い印も、そのまま引き当てられる）。
  // 後ろから処理すると、同じテキストノードを2回切っても先に確定した位置がズレない。
  const targets = marks
    .map((m, i) => {
      const quote = normalizeQuote(m.quote);
      const at = quote ? indexOfNth(text, quote, m.nth) : -1;
      // 正規化テキストの [at, at+len) を、切り出しに使う生テキストの範囲へ戻す。
      return { i, at, from: at < 0 ? -1 : rawAt[at], to: at < 0 ? -1 : rawAt[at + quote.length - 1] + 1 };
    })
    .filter((x) => x.at >= 0)
    .sort((a, b) => b.from - a.from);

  for (const target of targets) {
    const end = target.to;
    for (const s of spans) {
      const node = s.node;
      const nodeEnd = s.start + (node.nodeValue || "").length;
      if (nodeEnd <= target.from || s.start >= end) continue; // 重ならない
      if (!node.parentNode) continue; // 直前の切り出しで置き換わっている
      // 段落や箇条書きをまたぐ引用は、ブロックの隙間の空白ノードも範囲に入る。塗っても
      // 見た目は変わらないうえ <ul> の直下に <mark> を作ることになるので、そこは飛ばす。
      if (!/\S/.test(node.nodeValue || "")) continue;
      const from = Math.max(0, target.from - s.start);
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
