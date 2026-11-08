// Source-line anchors for a rendered Markdown preview: the split view's scroll sync
// (splitScrollSync.ts) maps the editor's line to a preview offset through them.
//
// marked's HTML carries no positions, so the rendered blocks are matched back to the
// lexer's tokens after the fact: the token stream and the root's element children come out
// in the same order, and a block whose tag does not match its token (raw HTML, which renders
// anything, or nothing at all) is simply left without an anchor. The match is greedy and
// only moves forward, so one stray element costs an anchor, never the ones after it.
//
// It must run on the renderer's own output, before any post-processing reshapes the tree
// (front matter panel, mermaid replacing its <pre>): those happen after this.
import type { Token, Tokens } from "marked";
import { marked } from "../../../lib/markdown.ts";

/** The attribute holding a block's 1-based line in the source the user edits. */
export const SOURCE_LINE_ATTR = "data-src-line";

/** Which element a block token renders as, or null when it has no fixed shape. */
function expectedTag(token: Token): string | null {
  switch (token.type) {
    case "heading":
      return "H" + (token as Tokens.Heading).depth;
    case "paragraph":
      return "P";
    case "code":
      return "PRE";
    case "blockquote":
      return "BLOCKQUOTE";
    case "list":
      return (token as Tokens.List).ordered ? "OL" : "UL";
    case "table":
      return "TABLE";
    case "hr":
      return "HR";
    default:
      return null; // html, text (a tight list item's bare text), space, def
  }
}

/** Line number (0-based) of every offset, by counting the newlines before it. */
function lineIndex(text: string): (offset: number) => number {
  const starts = [0];
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) starts.push(i + 1);
  return (offset) => {
    let lo = 0;
    let hi = starts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (starts[mid]! <= offset) lo = mid;
      else hi = mid - 1;
    }
    return lo;
  };
}

/** Map a 0-based line of a repaired body (repairFullwidthTables) back to the line it came
 *  from, given the lines the repair inserted. An inserted line maps to the one above it. */
export function insertedLineMap(inserted: readonly number[]): (line: number) => number {
  if (!inserted.length) return (line) => line;
  return (line) => {
    let before = 0;
    for (const at of inserted) if (at <= line) before++;
    return Math.max(0, line - before);
  };
}

/** Where the first non-blank line of `raw` occurs in `src` within [from, to), or -1. Nested
 *  tokens carry de-indented raw text, so only that first line, trimmed, can be searched. */
function findRaw(src: string, raw: string, from: number, to: number): number {
  if (src.startsWith(raw, from)) return from;
  const first = raw.split("\n").find((line) => line.trim() !== "")?.trim();
  if (!first) return -1;
  const at = src.indexOf(first, from);
  return at >= 0 && at < to ? at : -1;
}

export interface SourceLineOptions {
  gfm?: boolean;
  breaks?: boolean;
  /** Lines in front of `body` in the edited source (a front matter block). */
  lineOffset?: number;
  /** Maps a 0-based line of `body` to the 0-based line of the text the user wrote. */
  mapLine?: (line: number) => number;
}

/** Tag the blocks of `root`, rendered from `body`, with their 1-based source lines. List
 *  items are tagged too, and their own blocks within: a long list is often most of a
 *  document. Blockquote contents are not — their raw text has lost its `>` prefixes. */
export function tagSourceLines(root: HTMLElement, body: string, options: SourceLineOptions = {}): void {
  let tokens: Token[];
  try {
    tokens = marked.lexer(body, { gfm: options.gfm ?? true, breaks: options.breaks ?? false });
  } catch {
    return; // the render itself got past the same input; an anchor-less preview just does not sync
  }
  const lineOf = lineIndex(body);
  const offset = options.lineOffset ?? 0;
  const mapLine = options.mapLine ?? ((line: number) => line);
  const tag = (el: Element, at: number) => el.setAttribute(SOURCE_LINE_ATTR, String(mapLine(lineOf(at)) + offset + 1));

  const walk = (list: Token[], children: Element[], from: number, to: number) => {
    let pos = from;
    let ci = 0;
    for (const token of list) {
      const at = findRaw(body, token.raw, pos, to);
      // A raw found only by its first line has an unknown length; step past that line alone.
      if (at >= 0) pos = at + (body.startsWith(token.raw, at) ? token.raw.length : 1);
      const want = expectedTag(token);
      if (!want || at < 0) continue;
      let k = ci;
      while (k < children.length && children[k]!.tagName !== want) k++;
      if (k === children.length) continue;
      const el = children[k]!;
      ci = k + 1;
      tag(el, at);
      // A nested list's raw is de-indented, so its length undercounts the span it covers in
      // the source; only a verbatim raw gives a trustworthy end, otherwise the parent's does.
      const verbatim = body.startsWith(token.raw, at);
      if (token.type === "list") walkList(token as Tokens.List, el, at, verbatim ? at + token.raw.length : to);
    }
  };

  const walkList = (list: Tokens.List, el: Element, from: number, to: number) => {
    const items = [...el.children].filter((child) => child.tagName === "LI");
    if (items.length !== list.items.length) return; // not the list we think it is
    let pos = from;
    list.items.forEach((item, i) => {
      const at = findRaw(body, item.raw, pos, to);
      if (at < 0) return;
      const end = body.startsWith(item.raw, at) ? Math.min(to, at + item.raw.length) : to;
      pos = end === to ? at + 1 : end;
      const li = items[i]!;
      tag(li, at);
      // The item's first block shares the item's line; only what follows it adds anchors.
      walk(item.tokens, [...li.children], at, end);
    });
  };

  walk(tokens, [...root.children], 0, body.length);
}
