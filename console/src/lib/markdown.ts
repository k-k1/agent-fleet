import { load, YAML11_SCHEMA, mergeTag } from "js-yaml";
import { Marked, Tokenizer, type Token, type Tokens } from "marked";

export interface YamlFrontMatter {
  attributes: Record<string, unknown>;
  body: string;
  // The block is not valid YAML and was read as flat `key: value` lines instead
  // (see parseFlatEntries). The viewer says so out loud: every other tool still
  // sees a broken document.
  lenient?: boolean;
}

// Read a YAML front matter block at the start of a Markdown document. Marked
// does not consume front matter by default, so the viewer handles it before
// rendering the body.
//
// Only a complete, mapping-shaped block at byte zero is recognized. This leaves
// thematic breaks and incomplete front matter (while a chat message is streaming)
// as ordinary Markdown. A complete block YAML rejects gets one more chance as flat
// `key: value` lines (parseFlatEntries) before it is left as Markdown too.
export interface TableRepair {
  body: string;
  // Indexes, in document order among all table blocks found, of the tables that were
  // repaired — so a caller can point at the rendered <table> that needed it.
  repaired: number[];
  // How many table blocks were found in all. A caller compares this against the number
  // of <table> elements the renderer produced before trusting the indexes above.
  total: number;
}

// A pipe as GFM wants it, plus the two lookalikes a Japanese IME hands you: a table
// typed alongside Japanese cell text comes out with U+FF5C ｜ throughout, looks aligned
// in the editor, and renders as one run-on paragraph. Documents written that way also
// tend to drop the delimiter row entirely.
const PIPE = /[|｜￨]/;
const PIPES = /[|｜￨]/g;
// GFM needs one dash or more; the fullwidth dashes travel with the fullwidth pipes.
const DELIMITER_CELL = /^\s*:?[-－ー―‐]+:?\s*$/;
// Without a delimiter row, pipe-framed lines are only believed to be a table once this
// many agree on a column count — below that a line of prose could qualify by accident.
const MIN_ROWS_WITHOUT_DELIMITER = 3;

// Split a pipe-framed line into its cells, or null if it is not shaped like a table row.
// Escaped \| inside a cell would be split too, but a block only reaches the repair path
// when it holds no ASCII pipe at all, so it cannot contain one.
function tableRow(line: string | undefined): string[] | null {
  if (line === undefined || !/^ {0,3}\S/.test(line)) return null; // 4 spaces in = code
  const text = line.trim();
  if (text.length < 3 || !PIPE.test(text[0]) || !PIPE.test(text[text.length - 1])) return null;
  return text.slice(1, -1).split(PIPES);
}

const isDelimiterRow = (cells: string[]) => cells.every((cell) => DELIMITER_CELL.test(cell));
// The mark of a mistyped row: fullwidth pipes and not one ASCII pipe to be found.
const isFullwidthRow = (line: string) => /[｜￨]/.test(line) && !line.includes("|");
// Both widths on one row: the ASCII ones are the separators and the fullwidth one is
// cell content, deliberately — the only way to put a vertical bar in a cell without
// splitting it. docs/log/54-opencode-console-oauth.md does exactly that.
const mixesPipeWidths = (line: string) => /[｜￨]/.test(line) && line.includes("|");

// Repair tables written with fullwidth pipes, and supply a delimiter row where one is
// missing, before the Markdown reaches the renderer. Returns null when nothing needed
// repairing — the overwhelmingly common case, decided by a single scan of the source.
//
// A block is repaired when no row of it mixes the two widths and at least one row is
// purely fullwidth; only those purely fullwidth rows are rewritten. That covers a table
// typed wholly in fullwidth, and the half-converted ones where an editor fixed the
// delimiter row, or every row but the header, and stopped. A row that mixes widths is
// read as deliberate and stops the whole block from being touched.
export function repairFullwidthTables(source: string): TableRepair | null {
  if (!/[｜￨]/.test(source)) return null;
  const lines = source.split("\n");
  const repaired: number[] = [];
  let total = 0;
  let fence = "";

  for (let i = 0; i < lines.length; i++) {
    const marker = lines[i].match(/^ {0,3}(`{3,}|~{3,})/);
    if (marker) {
      if (!fence) fence = marker[1][0];
      else if (marker[1][0] === fence) fence = "";
      continue;
    }
    if (fence) continue;

    const header = tableRow(lines[i]);
    if (!header) continue;
    const next = tableRow(lines[i + 1]);
    const hasDelimiter = !!next && isDelimiterRow(next) && next.length === header.length;

    let end = hasDelimiter ? i + 2 : i + 1;
    while (end < lines.length) {
      const row = tableRow(lines[end]);
      // With no delimiter yet, the column count is the only evidence the block is a
      // table, and a delimiter-shaped line further down starts a different one.
      if (!row || (!hasDelimiter && (row.length !== header.length || isDelimiterRow(row)))) break;
      end++;
    }
    // A block with no delimiter row that is too short to be worth supplying one with
    // stays prose however its pipes are typed — rewriting it would change nothing a
    // reader can see, and counting it would put `total` out of step with the tables the
    // renderer actually produces.
    const synthesize = !hasDelimiter && end - i - 1 >= MIN_ROWS_WITHOUT_DELIMITER;
    if (!hasDelimiter && !synthesize) {
      i = end - 1;
      continue;
    }
    total++;

    // The delimiter row is left out of the "is anything wrong here" question: it carries
    // no text, so an ASCII one proves nothing about how the rest was typed.
    const content = lines.slice(i, end).filter((_, offset) => !(hasDelimiter && offset === 1));
    if (content.some(isFullwidthRow) && !lines.slice(i, end).some(mixesPipeWidths)) {
      for (let k = i; k < end; k++) {
        if (!isFullwidthRow(lines[k])) continue;
        lines[k] = lines[k].replace(PIPES, "|");
        // Fullwidth dashes are only ever dashes on the delimiter row, whose cells match
        // DELIMITER_CELL and so hold nothing else. In a content cell ー is a prolonged
        // sound mark, and rewriting it turns コード into コ-ド.
        if (hasDelimiter && k === i + 1) lines[k] = lines[k].replace(/[－ー―‐]/g, "-");
      }
      if (synthesize) {
        lines.splice(i + 1, 0, `|${Array(header.length).fill("---").join("|")}|`);
        end++;
      }
      repaired.push(total - 1);
    }
    i = end - 1;
  }

  return repaired.length ? { body: lines.join("\n"), repaired, total } : null;
}

// One `key: value` line, the way a human writes front matter: the key at column
// zero (so nothing nested), a non-empty value on the same line. A leading `#` or
// `-` is a comment or a list item \u2014 shapes this fallback does not claim to read.
const FLAT_ENTRY = /^([^\s#-][^:]*?)[\t ]*:[\t ]+(\S.*)$/;
// A value opening with a YAML structure indicator \u2014 a flow collection, a block
// scalar, an anchor / alias / tag \u2014 was meant as real YAML, and it is broken.
// Reading it as a string would show the reader something nobody wrote, so leave
// the whole block alone instead ("title: [" stays prose, as it always did).
const STRUCTURED_VALUE = /^[[\]{}|>&*!%,#]/;
// Surrounding quotes are the author's, not the value's \u2014 strip one matching pair.
const unquote = (value: string): string =>
  /^"[^"]*"$/.test(value) || /^'[^']*'$/.test(value) ? value.slice(1, -1) : value;

// Read a front matter block that YAML rejected as flat `key: value` lines.
//
// Why bother: YAML reserves ` and @ as the FIRST character of a plain scalar, so
// an entirely ordinary line \u2014 \u5099\u8003: `\u30EC\u30D3\u30E5\u30FC_\u8F9B\u53E3\u7DE8\u96C6\u8005.md` \u3068\u306F\u5F79\u5272\u304C\u9055\u3046 \u2014 throws,
// and the whole block then renders as one run-on paragraph of prose above the
// document. Read line by line it is exactly what the author meant.
//
// Only a block where every line is a flat entry (blank and comment lines aside)
// is accepted; anything with nesting, lists or block scalars returns null and
// keeps the old behavior of rendering as Markdown.
function parseFlatEntries(yaml: string): Record<string, unknown> | null {
  const attributes: Record<string, unknown> = {};
  for (const line of yaml.split(/\r?\n/)) {
    if (/^[\t ]*$/.test(line) || /^[\t ]*#/.test(line)) continue;
    const entry = line.match(FLAT_ENTRY);
    if (!entry) return null;
    const value = entry[2].trim();
    if (STRUCTURED_VALUE.test(value)) return null;
    attributes[entry[1]] = unquote(value);
  }
  return Object.keys(attributes).length ? attributes : null;
}

// YAML11_SCHEMA carries the 1.1 tags but not the merge key, which js-yaml 5 keeps separate.
const FRONT_MATTER_SCHEMA = YAML11_SCHEMA.withTags(mergeTag);

// Front matter is read as YAML 1.1, which is what js-yaml 4 did by default and what the
// front-matter convention grew up on: `date: 2026-09-06` is a timestamp (renderFrontMatter
// prints Dates as ISO), `<<` merges, `!!set` loads. js-yaml 5 made YAML 1.2 / CORE_SCHEMA
// the default instead, where the date is the plain string and the other two are an error —
// and an error here is not a fallback but a disappearance, since load() throwing drops the
// whole block to parseFlatEntries, which rejects anything nested and returns null. Pinning
// the schema keeps every document that rendered before rendering the same; changing how
// dates read is a product decision, not something a dependency bump gets to make.
export function splitYamlFrontMatter(source: string): YamlFrontMatter | null {
  const match = source.match(/^\uFEFF?---[\t ]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)/);
  if (!match) return null;
  const yaml = match[0]
    .replace(/^\uFEFF?---[\t ]*\r?\n/, "")
    .replace(/\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)$/, "");
  const body = source.slice(match[0].length);
  let attributes: unknown;
  try {
    attributes = load(yaml, { schema: FRONT_MATTER_SCHEMA });
  } catch {
    const flat = parseFlatEntries(yaml);
    return flat ? { attributes: flat, body, lenient: true } : null;
  }
  if (!attributes || Array.isArray(attributes) || typeof attributes !== "object") return null;
  return { attributes: attributes as Record<string, unknown>, body };
}

// A destination a link reference definition could plausibly point at. Either printable
// ASCII with no space — a URL, a path, a bare filename, everything definitions have
// always been written with — or an opening that says "target" out loud, which is what
// keeps `https://ja.wikipedia.org/wiki/日本語` and `/docs/日本語.md` working.
const ASCII_DESTINATION = /^[\x21-\x7e]+$/;
const EXPLICIT_DESTINATION = /^(?:[a-zA-Z][a-zA-Z0-9+.-]*:|\/|\.{1,2}\/|[#?])/;

export function isLinkDestination(destination: string): boolean {
  // <…> is CommonMark's unambiguous form: the author already said this is a target.
  return destination.startsWith("<") || ASCII_DESTINATION.test(destination) || EXPLICIT_DESTINATION.test(destination);
}

// Raw HTML reaches the sanitizer verbatim, and DOMPurify deletes every element it does not
// allow — the tag itself, silently. So an angle-bracket placeholder in ordinary prose does
// not render oddly, it renders as NOTHING: a mirror turn reading
//   <svn repo url>/trunk をチェックアウトして
// came out as `/trunk をチェックアウトして`, with no sign that three words were dropped, because
// CommonMark reads `<svn repo url>` as an open tag with two valueless attributes. Placeholders
// of exactly that shape — <id>, <name>, <your-token> — are all over this repo's own docs.
//
// So a tag is markup only when its name is an element the viewer would actually render;
// anything else falls through to the paragraph tokenizer, which escapes it, and the reader
// sees what the author typed. Comments, doctypes and CDATA are not tags and keep their old
// behavior — a document hiding a note in <!-- --> means it.
//
// The list is what survives DOMPurify in a document fragment, which is narrower than "HTML
// element": <html>/<head>/<body> are dropped by the parser, and <script>/<style>/<iframe>/
// <template> by the sanitizer, so honoring those would only reinstate the disappearance for
// prose that names them. markdown.dom.test.tsx probes every entry against the sanitizer,
// because a dependency bump may not keep this list true.
//
// svg and math are the block-level exception: a diagram written as a block is captured whole
// by the block tokenizer, children and all, so the root alone is enough to let it through.
// Their child names (<path>, <text>, <line>) stay out — they read as placeholders far more
// often than as markup, and the block form does not need them.
export const HTML_TAGS = new Set(
  ("a abbr acronym address area article aside audio b bdi bdo big blockquote br button canvas " +
    "caption center cite code col colgroup data datalist dd del details dfn dialog dir div dl " +
    "dt em fieldset figcaption figure font footer form h1 h2 h3 h4 h5 h6 header hgroup hr i " +
    "img input ins kbd label legend li main map mark math menu meter nav nobr ol " +
    "optgroup option output p picture pre progress q rp rt ruby s samp search section select " +
    "slot small source span strike strong sub summary sup svg table tbody td textarea tfoot " +
    "th thead time tr track tt u ul var video wbr").split(" "),
);

// The name at the head of a tag-shaped run, open or closing. It fails to match anything that
// is not a tag (a comment, a doctype, CDATA) — which this rule then leaves alone.
const TAG_NAME = /^<\/?([a-zA-Z][a-zA-Z0-9-]*)[\s/>]/;

export function isRenderedHtmlTag(raw: string): boolean {
  const name = TAG_NAME.exec(raw);
  return !name || HTML_TAGS.has(name[1].toLowerCase());
}

// An opening tag that is never closed does not stay where it was written. The HTML parser
// closes a formatting element at the end of its paragraph and re-opens it in the next one, so
// prose that merely names `<a download>` colours every later paragraph as a link — measured on
// the sanitized output, so DOMPurify does not prevent it. These 14 are the elements the parser
// re-opens (the HTML Standard's "formatting" category). Containers such as <div> stay open
// on purpose, and an inline <span> is closed with its paragraph, so neither is touched.
//
// Pairing is decided on lexed tokens, never on the source text: a `</a>` inside a code span,
// a comment or an escape is a different token and cannot close anything. An opener without a
// closer is turned back into text, which is what the author typed. The stock tokenizer is left
// alone on purpose — it would set `inLink` on `<a` before we could know.
const FORMATTING_TAGS = new Set("a b big code em font i nobr s small strike strong tt u".split(" "));

// Two more kinds of tag change what a closer means, so they are read as well:
// - Raw-text and RCDATA elements: everything up to their own end tag is text, so a `</a>` in
//   there closes nothing. (Only some reach the parser as elements — `<script>` is vetoed to text
//   by HTML_TAGS above and DOMPurify drops it from a block — but a block is scanned whole.)
// - Scope markers: a formatting element is not closed from inside a table cell, caption,
//   object, applet or template — the parser's active-formatting list stops there. A cell or
//   caption only counts inside a <table> (the parser drops it elsewhere), and it ends at its
//   own end tag or with the table: the end tags are optional.
//   <marquee> is deliberately not honored as markup at all (see HTML_TAGS): it scrolls its text.
const RAW_TEXT_TAGS = new Set("script style textarea title xmp iframe noembed noframes".split(" "));
const SCOPE_TAGS = new Set("applet caption object td template th".split(" "));
const CELL_TAGS = new Set("caption td th".split(" "));

interface TagEvent {
  name: string;
  open: boolean;
}

function tagEvent(raw: string): TagEvent | null {
  const name = TAG_NAME.exec(raw)?.[1].toLowerCase();
  if (!name || !(FORMATTING_TAGS.has(name) || SCOPE_TAGS.has(name) || RAW_TEXT_TAGS.has(name) || name === "table")) return null;
  return { name, open: !raw.startsWith("</") };
}

// Pairs each closer with the nearest unmatched opener of the same name, never across a scope
// marker, and ignores everything inside a raw-text element; `finish` reports the openers
// nothing closed. A closer with no opener is left alone, as the HTML parser does.
class TagPairing {
  private open: { name: string; stray: () => void; at: number; marker?: boolean }[] = [];
  // Set while inside a raw-text element: the name whose end tag leaves it.
  rawText: string | null = null;
  // Matched pairs by position, for the caller that needs to know what lies between them.
  readonly pairs: { name: string; from: number; to: number }[] = [];
  // Closers that found no opener here; a block opener elsewhere may own them.
  readonly orphans: TagEvent[] = [];

  push(event: TagEvent, stray: () => void, at = 0): void {
    if (this.rawText) {
      if (!event.open && event.name === this.rawText) this.rawText = null;
      return;
    }
    if (event.open && RAW_TEXT_TAGS.has(event.name)) {
      this.rawText = event.name;
      return;
    }
    if (event.name === "table") {
      this.tableTag(event, at);
      return;
    }
    if (SCOPE_TAGS.has(event.name)) {
      if (CELL_TAGS.has(event.name) && !this.inTable()) return; // dropped by the parser, so no boundary
      if (event.open) this.open.push({ name: event.name, stray: () => {}, at, marker: true });
      else this.closeScope(event.name);
      return;
    }
    if (event.open) {
      this.open.push({ name: event.name, stray, at });
      return;
    }
    for (let i = this.open.length - 1; i >= 0; i--) {
      // A scope boundary stops the closer for good: it is not offered to the document either,
      // because the parser would not let it out of the scope.
      if (this.open[i].marker) return;
      if (this.open[i].name === event.name) {
        this.pairs.push({ name: event.name, from: this.open[i].at, to: at });
        this.open.splice(i, 1);
        return;
      }
    }
    this.orphans.push(event);
  }

  private inTable(): boolean {
    return this.open.some((o) => o.name === "table");
  }

  // The end tag of a cell is optional, so a cell boundary is only known to be over when its
  // table ends: `</table>` drops every boundary opened since `<table>`, and what was left open
  // in there is stray. Rows and sections need no handling of their own for the same reason.
  private tableTag(event: TagEvent, at: number): void {
    if (event.open) {
      this.open.push({ name: "table", stray: () => {}, at });
      return;
    }
    const table = this.open.map((o) => o.name).lastIndexOf("table");
    if (table >= 0) for (const o of this.open.splice(table)) o.stray();
  }

  // The end tag of a scope element: whatever was left open inside it is closed with it, and
  // still reported as stray.
  private closeScope(name: string): void {
    const at = this.open.map((o) => o.marker && o.name === name).lastIndexOf(true);
    if (at < 0) return;
    for (const o of this.open.splice(at)) o.stray();
  }

  finish(): void {
    for (const o of this.open) o.stray();
    this.open = [];
  }
}

// One inline unit (paragraph, heading, list-item text, table cell) is paired as a whole,
// recursing into emphasis and links so `*<a href="x">y*</a>` still counts as closed. The walk
// is flat and in document order; `at` is a token's position in it.
type InlineItem = { tag: TagEvent; token: Token } | { url: Tokens.Link };

function collectInline(tokens: Token[], items: InlineItem[]): void {
  for (const t of tokens) {
    // An image's children become its alt attribute, never tags in the body.
    if (t.type === "image") continue;
    if (t.type === "html") {
      const tag = tagEvent(t.raw);
      if (tag) items.push({ tag, token: t });
    } else if (t.type === "link" && t.raw === t.text) {
      items.push({ url: t as Tokens.Link }); // a bare URL (GFM autolink extension)
    } else if ("tokens" in t && Array.isArray(t.tokens)) {
      collectInline(t.tokens, items);
    }
  }
}

function toText(t: Token): void {
  const token = t as Tokens.Generic;
  token.type = "text";
  token.text = t.raw;
  delete token.escaped;
  delete token.tokens;
  delete token.href;
}

// `doc` is where a closer this unit cannot pair is offered, so `<a href>` alone on a line can
// still be closed by `</a>` in a later paragraph. The reverse — an inline opener closed by a
// later block — is not honored: a paragraph's own stray opener must not reach past it. A table
// cell passes no `doc`: the HTML parser stops a closer at the cell boundary.
//
// The stock tokenizer would otherwise decide bare-URL linking by `inLink`, set on `<a` before
// anyone knows whether it is closed; instead bare URLs are always lexed, and the ones that end
// up inside a real `<a>…</a>` are turned back into text here, so they never nest an anchor.
function pairUnit(tokens: Token[], doc?: TagPairing): void {
  const items: InlineItem[] = [];
  collectInline(tokens, items);
  const unit = new TagPairing();
  items.forEach((item, at) => {
    if ("tag" in item) unit.push(item.tag, () => toText(item.token), at);
  });
  unit.finish();
  for (const { name, from, to } of unit.pairs) {
    if (name !== "a") continue;
    for (let at = from + 1; at < to; at++) {
      const item = items[at];
      if ("url" in item) toText(item.url);
    }
  }
  if (doc) for (const event of unit.orphans) doc.push(event, () => {});
}

// A comment (skipped) or a tag with quoted attributes, inside block-level raw HTML. A comment
// ends as the HTML tokenizer ends it: `<!-->` and `<!--->` are complete, `--!>` also closes,
// and an unclosed one runs to the end.
const HTML_RUN = /<!--(?:-?>|[\s\S]*?--!?>|[\s\S]*$)|<\/?[a-zA-Z][a-zA-Z0-9-]*(?:"[^"]*"|'[^']*'|[^'">])*>/g;

// Raw HTML blocks are paired across the whole document: `<a href>` alone on a line may be
// closed by a later block. Strays are escaped once the document is done, right to left so
// the earlier offsets stay valid.
function pairBlockHtml(token: Tokens.HTML, pairing: TagPairing, escapes: (() => void)[]): void {
  const strays: number[] = [];
  const run = new RegExp(HTML_RUN);
  for (let m = run.exec(token.text); m; m = run.exec(token.text)) {
    const event = tagEvent(m[0]);
    if (event) pairing.push(event, () => strays.push(m!.index));
    // Inside a raw-text element nothing is a tag until its end tag; look for that, not for
    // the next `<`.
    if (pairing.rawText) {
      const end = new RegExp(`</${pairing.rawText}(?=[\\s/>])`, "ig");
      end.lastIndex = run.lastIndex;
      const hit = end.exec(token.text);
      if (!hit) break;
      run.lastIndex = hit.index;
    }
  }
  escapes.push(() => {
    let text = token.text;
    for (const at of strays.sort((a, b) => b - a)) text = text.slice(0, at) + "&lt;" + text.slice(at + 1);
    token.text = text;
  });
}

function walkBlocks(tokens: Token[], pairing: TagPairing, escapes: (() => void)[]): void {
  for (const t of tokens) {
    switch (t.type) {
      case "paragraph":
      case "heading":
      case "text":
        pairUnit((t as Tokens.Paragraph).tokens ?? [], pairing);
        break;
      case "html":
        pairBlockHtml(t as Tokens.HTML, pairing, escapes);
        break;
      case "list":
        for (const item of (t as Tokens.List).items) walkBlocks(item.tokens, pairing, escapes);
        break;
      case "blockquote":
        walkBlocks((t as Tokens.Blockquote).tokens, pairing, escapes);
        break;
      case "table": {
        const table = t as Tokens.Table;
        for (const cell of [...table.header, ...table.rows.flat()]) pairUnit(cell.tokens);
        break;
      }
    }
  }
}

// The pairing above imitates the HTML parser, and the parser has more states than it models:
// raw text across blocks, <template>, a table with no cell, script's double escape. Rather than
// chase them, the result is checked against the real parser. A sentinel element is put after the
// whole document; if it ends up inside a formatting element, an opener nothing closed has
// leaked, whatever the cause. The opener whose removal shortens that chain is turned into text,
// one at a time, until the chain is empty or no single removal helps. The check runs only when
// some formatting tag survived the pairing, and needs a DOM (it is skipped without one).
const PROBE = '<span data-af-probe=""></span>';
const MAX_PROBES = 64;

// Formatting ancestors of the sentinel when `tokens` are rendered, i.e. how many open
// formatting elements reach the end of the document.
function leakDepth(tokens: Token[]): number {
  const html = marked.parser(tokens) + PROBE;
  const probe = new DOMParser().parseFromString(html, "text/html").querySelector("[data-af-probe]");
  let depth = 0;
  for (let e = probe?.parentElement; e; e = e.parentElement) if (FORMATTING_TAGS.has(e.localName)) depth++;
  return depth;
}

// Every formatting opener still written as markup, with the means to make it text.
function openers(tokens: Token[], found: (() => () => void)[] = []): (() => () => void)[] {
  const inline = (list: Token[]): void => {
    for (const t of list) {
      if (t.type === "image") continue;
      if (t.type === "html") {
        const e = tagEvent(t.raw);
        if (e?.open && FORMATTING_TAGS.has(e.name)) found.push(() => {
          const before = { ...(t as Tokens.Generic) };
          toText(t);
          return () => Object.assign(t, before, { type: before.type });
        });
      } else if ("tokens" in t && Array.isArray(t.tokens)) inline(t.tokens);
    }
  };
  for (const t of tokens) {
    if (t.type === "html") {
      const token = t as Tokens.HTML;
      const run = new RegExp(HTML_RUN);
      for (let m = run.exec(token.text); m; m = run.exec(token.text)) {
        const e = tagEvent(m[0]);
        if (!e?.open || !FORMATTING_TAGS.has(e.name)) continue;
        const at = m.index;
        found.push(() => {
          const before = token.text;
          token.text = before.slice(0, at) + "&lt;" + before.slice(at + 1);
          return () => { token.text = before; };
        });
      }
    } else if (t.type === "list") for (const item of (t as Tokens.List).items) openers(item.tokens, found);
    else if (t.type === "blockquote") openers((t as Tokens.Blockquote).tokens, found);
    else if (t.type === "table") {
      const table = t as Tokens.Table;
      for (const cell of [...table.header, ...table.rows.flat()]) inline(cell.tokens);
    } else if ("tokens" in t && Array.isArray(t.tokens)) inline(t.tokens);
  }
  return found;
}

function verifyAgainstParser(tokens: Token[]): void {
  if (typeof DOMParser === "undefined") return;
  let candidates = openers(tokens);
  if (candidates.length === 0) return;
  let depth = leakDepth(tokens);
  while (depth > 0 && candidates.length > 0) {
    let best = -1;
    let bestDepth = depth;
    for (let i = 0; i < Math.min(candidates.length, MAX_PROBES); i++) {
      const undo = candidates[i]();
      const d = leakDepth(tokens);
      undo();
      if (d < bestDepth) {
        best = i;
        bestDepth = d;
      }
    }
    if (best < 0) return;
    candidates[best]();
    depth = bestDepth;
    // Escaping inside a block shifts the offsets of its later candidates: look again.
    candidates = openers(tokens);
  }
}

export function neutralizeStrayFormattingTags(tokens: Token[]): Token[] {
  const doc = new TagPairing();
  const escapes: (() => void)[] = [];
  walkBlocks(tokens, doc, escapes);
  doc.finish();
  for (const apply of escapes) apply();
  verifyAgainstParser(tokens);
  return tokens;
}

// CommonMark decides whether `**` opens or closes emphasis from the two characters around
// it: a delimiter next to punctuation only counts when the character on its other side is
// whitespace or punctuation too. Every CJK bracket, 、。！？…・ and every fullwidth form is
// Unicode punctuation, and Japanese prose has no spaces to rescue them — so
//   あ**「強調」**です     **強調。**続く     あ~~「取り消し」~~です
// come out as literal asterisks and tildes. The author sees plain text with the markers
// showing and no way to tell why; the identical sentence in English works.
//
// The fix, in the `*` and `~~` delimiter rules only: count ASCII punctuation as
// punctuation and nothing else, so a CJK mark counts as ordinary text the way the kanji
// beside it does. Because those classes are identical to marked's over the ASCII range, a
// document written wholly in ASCII cannot parse differently than before.
//
// That reading is applied as a SECOND ATTEMPT, never as a replacement: marked's own rules
// run first and whatever they find is kept untouched, and only a position where they found
// no emphasis at all is read again with the relaxed ones. The difference is not
// hypothetical — relaxing outright loses `**\`code\`**。`, where the closing run sits
// between an ASCII backtick and a 。 and stops being able to close. Second-attempt means
// the fix can only ever add emphasis that was not being rendered.
//
// Deliberately left alone:
//   - `_` (emStrongRDelimUnd), whose intraword rule is what keeps a filename like
//     Ph0_声の増量設計.md from turning into emphasis mid-sentence. The retry below is
//     entered for `*` only.
//   - `punctuation`, the "character BEFORE the opener" test, which is permissive: it lets
//     an opener follow punctuation, so narrowing it there would lose emphasis, not gain it.
const ASCII_PUNCT = "!-\\/:-@\\[-`{-~";
// The three character classes marked substitutes into its delimiter rules, verbatim.
// Rewriting the built regexes (rather than re-deriving the rules ourselves) keeps every
// other part of them — the rule-of-three bookkeeping, the `~` guards — marked's own. If an
// upgrade spells these differently the replacements simply stop matching, so the tests
// fail loudly instead of the viewer quietly regressing.
const CLASS_REWRITES: [RegExp, string][] = [
  [/\[\^\\s\\p\{P\}\\p\{S\}\]/g, `[^\\s${ASCII_PUNCT}]`], // notPunctSpace
  [/\[\\s\\p\{P\}\\p\{S\}\]/g, `[\\s${ASCII_PUNCT}]`], // punctSpace
  [/\[\\p\{P\}\\p\{S\}\]/g, `[${ASCII_PUNCT}]`], // punct
];
// Order matters: the negated class contains the other two as substrings.
export function asciiPunctuationRule(rule: RegExp): RegExp {
  let source = rule.source;
  for (const [pattern, replacement] of CLASS_REWRITES) source = source.replace(pattern, replacement);
  return source === rule.source ? rule : new RegExp(source, rule.flags);
}

// Where a bare URL has to stop. GFM's autolink rule runs to the next whitespace or `<` and
// nothing else (`[^\s<]*`), and its backpedal only hands back trailing ASCII punctuation.
// Japanese prose has no space to stop on and sets its punctuation flush against the URL, so
//   https://github.com/k-k1/agent-fleet/pull/727（base は develop）
// linked `…/pull/727（base`, percent-encoding the fullwidth paren and the word after it into
// the href: the reader loses the working link AND the word, and 、。」・… all do the same.
//
// So a bare URL ends at the first non-ASCII punctuation or symbol as well. Letters are not
// touched, which is what keeps https://ja.wikipedia.org/wiki/日本語 whole — and 人々, 〇〇, コード
// with it, since 々〆〇ー are letters (Lm/Lo/Nl), not punctuation. Nothing changes over the
// ASCII range: marked's own backpedal still decides where `…/x).` ends. An author who really
// means a URL with a fullwidth character in it still has `<…>` and `[text](url)`, which are
// read by other rules entirely.
const URL_STOP = /(?![\x00-\x7f])[\p{P}\p{S}]/u;

type Rules = Tokenizer["rules"];
const CJK_FRIENDLY_RULES = ["emStrongLDelim", "emStrongRDelimAst", "delLDelim", "delRDelim"] as const;
// Marked hands the tokenizer a fresh `rules` object per Lexer, holding the shared rule set
// for the active options (gfm / breaks / pedantic) — which must not be mutated, or every
// other importer of "marked" would see it. So keep a rewritten copy per rule set, and one
// entry per copy as well: an inner inlineTokens() call re-enters the tokenizer while the
// relaxed set is installed, and has to be able to find its way back to marked's own.
const variants = new WeakMap<Rules, [stock: Rules, relaxed: Rules]>();

function rulePair(rules: Rules): [Rules, Rules] {
  const known = variants.get(rules);
  if (known) return known;
  const inline = { ...rules.inline };
  for (const name of CJK_FRIENDLY_RULES) {
    const rule = rules.inline[name];
    if (rule instanceof RegExp) inline[name] = asciiPunctuationRule(rule);
  }
  const pair: [Rules, Rules] = [rules, { ...rules, inline }];
  variants.set(pair[0], pair);
  variants.set(pair[1], pair);
  return pair;
}

// Run one of marked's own inline tokenizers against marked's rules, then — only if it
// found nothing — against the CJK-friendly ones.
function retryWithCjkRules<T>(
  tokenizer: Tokenizer,
  read: (this: Tokenizer) => T | undefined,
  relaxable: boolean,
): T | undefined {
  const [stock, relaxed] = rulePair(tokenizer.rules);
  try {
    tokenizer.rules = stock;
    const token = read.call(tokenizer);
    if (token || !relaxable) return token;
    tokenizer.rules = relaxed;
    return read.call(tokenizer);
  } finally {
    tokenizer.rules = stock;
  }
}

// The Marked instance the app renders with. A separate instance rather than the package
// singleton, because `marked.use()` would apply process-wide, and this tokenizer belongs
// to the viewer, not to anyone else who imports "marked" later.
//
// Why the tokenizer: `[label]: destination` is a link reference definition, and it renders
// as NOTHING — it only registers `label` for later `[label]` references. Japanese prose
// contains no ASCII space, so an ordinary note line
//   - [保留]: 幕間の再配置（一律不可・幕間ごと個別）／MED語彙拡張。
// matches that shape whole: the sentence is read as the destination, the list item comes
// out empty, and every later `[保留]` in the document silently becomes a link to it.
// Definitions are still honored — but only when the destination could actually be one.
export const marked = new Marked({
  hooks: {
    processAllTokens: neutralizeStrayFormattingTags,
  },
  tokenizer: {
    def(src) {
      // Marked's own rule decides whether this is a definition and where it ends;
      // re-implementing it here would drift (the destination and the title may sit on
      // the following lines) and would have to re-derive on its own that a fenced or
      // indented code block never reaches this point.
      const rule = this.rules.block.def as RegExp | undefined;
      const cap = rule?.exec(src);
      // No definition here, or the rule moved in a marked upgrade: `false` hands the
      // line back to the built-in tokenizer, i.e. the behavior we had before.
      if (!cap) return false;
      // `undefined` disables the rule for this line alone, so the block falls through to
      // paragraph / text and the author's line renders as it was written.
      return isLinkDestination(cap[2]) ? false : undefined;
    },
    // Inline raw HTML (`<br>`, `<b>`) and its block-level form. marked's own tokenizers still
    // decide what is a tag and where it ends; this only vetoes one whose name would be erased
    // downstream (see HTML_TAGS). `undefined` means "no tag here", so the run falls through to
    // text and is escaped, which is how the author wrote it.
    tag(src) {
      const before = this.lexer.state.inLink;
      const token = Tokenizer.prototype.tag.call(this, src);
      // `<a` / `</a>` flip `inLink` in the stock rule; neutralizeStrayFormattingTags decides
      // what a bare URL inside an anchor does once it knows which anchors are real.
      this.lexer.state.inLink = before;
      return token && !isRenderedHtmlTag(token.raw) ? undefined : token;
    },
    html(src) {
      const token = Tokenizer.prototype.html.call(this, src);
      return token && !isRenderedHtmlTag(token.raw) ? undefined : token;
    },
    // Bare URLs (the GFM autolink extension). marked's own tokenizer still decides what is a
    // URL, where it ends and how much source it consumes — it is only shown a source cut short
    // at the first character that cannot belong to one (see URL_STOP). Cutting the input rather
    // than editing the token keeps `raw` and the backpedal consistent by construction.
    url(src) {
      const stop = src.search(URL_STOP);
      return Tokenizer.prototype.url.call(this, stop > 0 ? src.slice(0, stop) : src);
    },
    // The two tokenizers that read `*`/`**` and `~~`. Each one is marked's own, called
    // twice at most — see retryWithCjkRules. `_` never takes the second attempt.
    emStrong(src, maskedSrc, prevChar) {
      return retryWithCjkRules(
        this,
        function () {
          return Tokenizer.prototype.emStrong.call(this, src, maskedSrc, prevChar);
        },
        src.startsWith("*"),
      );
    },
    del(src, maskedSrc, prevChar) {
      return retryWithCjkRules(
        this,
        function () {
          return Tokenizer.prototype.del.call(this, src, maskedSrc, prevChar);
        },
        src.startsWith("~"),
      );
    },
  },
});
