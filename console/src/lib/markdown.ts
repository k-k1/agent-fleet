import { load } from "js-yaml";

export interface YamlFrontMatter {
  attributes: Record<string, unknown>;
  body: string;
}

// Read a YAML front matter block at the start of a Markdown document. Marked
// does not consume front matter by default, so the viewer handles it before
// rendering the body.
//
// Only a complete, mapping-shaped block at byte zero is recognized. This leaves
// thematic breaks, invalid YAML, and incomplete front matter (while a chat
// message is streaming) as ordinary Markdown.
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

export function splitYamlFrontMatter(source: string): YamlFrontMatter | null {
  const match = source.match(/^\uFEFF?---[\t ]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)/);
  if (!match) return null;
  const yaml = match[0]
    .replace(/^\uFEFF?---[\t ]*\r?\n/, "")
    .replace(/\r?\n(?:---|\.\.\.)[\t ]*(?:\r?\n|$)$/, "");
  try {
    const attributes = load(yaml);
    if (!attributes || Array.isArray(attributes) || typeof attributes !== "object") return null;
    return { attributes: attributes as Record<string, unknown>, body: source.slice(match[0].length) };
  } catch {
    return null;
  }
}
