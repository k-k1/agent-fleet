import { describe, expect, it } from "vitest";
import DOMPurify from "dompurify";
import { HTML_TAGS } from "./markdown.ts";

// HTML_TAGS is the list of names raw HTML is honored for, and its whole justification is that
// the sanitizer MarkdownView runs afterwards keeps them: a name on the list that DOMPurify
// drops brings back the bug the list exists to fix — the tag and the words in it disappear
// with nothing shown in their place. DOMPurify's allow-list is its own and can move under a
// version bump, so ask it rather than trust the list.
//
// The question has to be asked in a parsing context: <td> outside a <table> is discarded by
// the HTML parser itself, which says nothing about the sanitizer's opinion of it.
const CONTEXT: Record<string, [string, string]> = {
  caption: ["<table>", "</table>"],
  col: ["<table>", "</table>"],
  colgroup: ["<table>", "</table>"],
  tbody: ["<table>", "</table>"],
  tfoot: ["<table>", "</table>"],
  thead: ["<table>", "</table>"],
  tr: ["<table>", "</table>"],
  td: ["<table><tr>", "</tr></table>"],
  th: ["<table><tr>", "</tr></table>"],
};

describe("HTML_TAGS", () => {
  it("names only elements the sanitizer keeps", () => {
    const dropped = [...HTML_TAGS].filter((tag) => {
      const [open, close] = CONTEXT[tag] ?? ["", ""];
      return !DOMPurify.sanitize(`${open}<${tag}>x</${tag}>${close}`).includes(`<${tag}`);
    });
    expect(dropped).toEqual([]);
  });

  // The other direction, on the names that made this worth doing: what the sanitizer erases
  // must not be on the list, or a document naming it in prose loses the word silently.
  it("leaves out what the sanitizer erases", () => {
    for (const tag of ["script", "style", "iframe", "object", "template", "html", "body", "svn"]) {
      expect(DOMPurify.sanitize(`<${tag}>x</${tag}>`)).not.toContain(`<${tag}`);
      expect(HTML_TAGS.has(tag)).toBe(false);
    }
  });
});
