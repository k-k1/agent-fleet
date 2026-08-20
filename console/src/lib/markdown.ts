import { load } from "js-yaml";
import { Marked } from "marked";

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
// splitting it. docs/54-opencode-console-oauth.md does exactly that.
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

export function splitYamlFrontMatter(source: string): YamlFrontMatter | null {
  const match = source.match(/^\uFEFF?---[\t ]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)/);
  if (!match) return null;
  const yaml = match[0]
    .replace(/^\uFEFF?---[\t ]*\r?\n/, "")
    .replace(/\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)$/, "");
  const body = source.slice(match[0].length);
  let attributes: unknown;
  try {
    attributes = load(yaml);
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
  },
});
