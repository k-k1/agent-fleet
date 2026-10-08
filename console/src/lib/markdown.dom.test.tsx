import { describe, expect, it } from "vitest";
import DOMPurify from "dompurify";
import { Marked } from "marked";
import { HTML_TAGS, marked } from "./markdown.ts";

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

// The path MarkdownView takes: parse, sanitize, then innerHTML. The leak is a property of the
// parsed DOM, so it is asserted there rather than on strings.
function render(renderer: Marked, source: string): HTMLElement {
  const el = document.createElement("div");
  el.innerHTML = DOMPurify.sanitize(renderer.parse(source) as string);
  return el;
}

const STOCK = new Marked();
const FORMATTING = "a b big code em font i nobr s small strike strong tt u".split(" ");

// The paragraph after the stray opener is the thing that must stay outside every formatting
// element; its text must survive too.
function leaks(el: HTMLElement, marker: string): boolean {
  const p = [...el.querySelectorAll("p")].find((x) => x.textContent === marker);
  if (!p) return true;
  return FORMATTING.some((name) => p.closest(name) !== null) || p.querySelector(FORMATTING.join(",")) !== null;
}

describe("a formatting tag that is never closed", () => {
  it.each(FORMATTING)("<%s> in prose does not wrap the next paragraph", (name) => {
    for (const open of [`<${name}>`, `<${name}/>`]) {
      const source = `Use ${open} here.\n\nSecond.`;
      // Positive control: the stock tokenizer really leaks, so the assertion can fail.
      expect(leaks(render(STOCK, source), "Second.")).toBe(true);
      const el = render(marked, source);
      expect(leaks(el, "Second.")).toBe(false);
      expect(el.textContent).toContain(open);
    }
  });

  it("keeps block-level raw HTML from leaking", () => {
    for (const source of [
      "<a download>\ntext\n\nSecond.",
      "<p>Use <a download>. End.</p>\n\nSecond.",
      "<div><a download>x</div>\n\nSecond.",
    ]) {
      expect(leaks(render(STOCK, source), "Second.")).toBe(true);
      expect(leaks(render(marked, source), "Second.")).toBe(false);
    }
  });

  it("does not take a closing tag in code, a comment or an attribute for a closer", () => {
    for (const source of [
      "Use <a download> then `</a>`\n\nSecond.",
      "Use <a download> <!-- </a> -->\n\nSecond.",
      'Use <a download> <span title="</a>">x</span>\n\nSecond.',
      "Use <a download> \\</a>\n\nSecond.",
    ]) {
      expect(leaks(render(marked, source), "Second.")).toBe(false);
    }
  });

  it("pairs each closer with one opener when the same name nests", () => {
    const el = render(marked, "Use <b>outer <b>inner</b> after\n\nSecond.");
    expect(leaks(el, "Second.")).toBe(false);
    expect(el.querySelectorAll("b").length).toBe(1);
  });

  it("covers headings, list items and table cells", () => {
    for (const source of [
      "# Title <a download>\n\nSecond.",
      "- item <a download>\n\nSecond.",
      "| h |\n| - |\n| <a download> |\n\nSecond.",
    ]) {
      expect(leaks(render(marked, source), "Second.")).toBe(false);
    }
  });

  it("leaves a stray tag in a Markdown link label from leaking", () => {
    expect(leaks(render(marked, "[<b>x](https://example.com)\n\nSecond."), "Second.")).toBe(false);
  });

  it("keeps pairs that are closed", () => {
    for (const [source, selector] of [
      ['Use <a href="x">one\ntwo</a> ok', "a[href]"],
      ["Use <B>bold</b> ok", "b"],
      ['*<a href="x">y*</a>', "a[href]"],
      ['<a href="x">\n\n![i](y)\n\n</a>', "a[href] img"],
      ["| h |\n| - |\n| <b>x</b> |", "td b"],
    ] as const) {
      expect(render(marked, source).querySelector(selector), source).not.toBeNull();
    }
  });

  it("leaves containers and void elements as they were", () => {
    expect(render(marked, "<div>\n\nx\n\n</div>").querySelector("div p")).not.toBeNull();
    expect(render(marked, "a<br>b").querySelector("br")).not.toBeNull();
    expect(render(marked, "<details><summary>s</summary>\n\nx\n\n</details>").querySelector("details")).not.toBeNull();
  });

  it("does not take an image alt for tags in the body", () => {
    expect(leaks(render(marked, "Use <a download> ![</a>](image.png)\n\nSecond."), "Second.")).toBe(false);
    // A made-up opener in the alt must not steal the closer of a real, closed pair.
    expect(render(marked, "Use <b>actual ![<b>](image.png) </b>").querySelector("b")).not.toBeNull();
  });

  it("lets a later paragraph close an opener that stood alone as a block", () => {
    const el = render(marked, '<a href="x">\n\none </a> after\n\nSecond.');
    expect([...el.querySelectorAll("a[href]")].map((x) => x.textContent)).toContain("one ");
    expect(el.textContent).not.toContain("<a");
  });

  it("shows every stray tag in a block, in place, and no anchor in a table cell", () => {
    const el = render(marked, "<div>before <B/> middle <A download> tail</div>");
    expect(el.textContent).toBe("before <B/> middle <A download> tail");
    expect(el.querySelector("a,b")).toBeNull();
    const cell = render(marked, "| h |\n| - |\n| x <a download> y |");
    expect(cell.querySelector("td")?.textContent).toBe("x <a download> y");
    expect(cell.querySelector("a")).toBeNull();
  });

  it("still links bare URLs after a stray <a>, and not inside a real anchor", () => {
    const stray = render(marked, "Use <a download> and https://example.com\n\nhttps://example.org");
    expect(stray.querySelectorAll("a[href]").length).toBe(2);
    const real = render(marked, '<a href="x">see https://example.com</a>');
    expect(real.querySelectorAll("a").length).toBe(1);
  });

  it("keeps a bare URL inside a real anchor as plain text, however the anchor is nested", () => {
    for (const source of [
      '<a href="x">see https://example.com</a>',
      '*<a href="x">see https://example.com*</a>',
      '**<a href="x">see https://example.com** after </a>',
    ]) {
      const anchors = [...render(marked, source).querySelectorAll("a")];
      expect(anchors.map((a) => a.getAttribute("href")), source).toEqual(["x", ...(source.includes("after") ? ["x"] : [])]);
    }
  });

  it("links bare URLs after a stray <a> whatever fake closer follows", () => {
    for (const source of [
      "Use <a download> https://example.com `</a>`\n\nhttps://example.org",
      "Use <a download> https://example.com <!-- </a> -->\n\nhttps://example.org",
      "Use <a download> https://example.com ![</a>](i.png)\n\nhttps://example.org",
    ]) {
      expect(render(marked, source).querySelectorAll("a[href^='https://']").length, source).toBe(2);
    }
  });

  it("does not let a table cell close an opener outside the table", () => {
    for (const source of [
      "<a download>\n\n| h |\n| - |\n| </a> |\n\nSecond.",
      "<b>\n\n| </b> |\n| - |\n| content |\n\nSecond.",
    ]) {
      expect(leaks(render(marked, source), "Second.")).toBe(false);
    }
  });

  // Hand-written HTML whose end tags the pairing must read the way the HTML parser does.
  it("does not take text inside a raw-text element for a closer", () => {
    for (const source of [
      '<div><a download>x<script>const s="</a>";</script></div>\n\nSecond.',
      "<div><a download>x<textarea></a></textarea></div>\n\nSecond.",
      "<div><a download>x<title></a></title></div>\n\nSecond.",
      "Use <a download><textarea></a></textarea>\n\nSecond.",
    ]) {
      expect(leaks(render(STOCK, source), "Second."), source).toBe(true);
      expect(leaks(render(marked, source), "Second."), source).toBe(false);
    }
  });

  it("reads comments as the HTML tokenizer does", () => {
    for (const source of [
      "<div><!--><a download>x --></div>\n\nSecond.",
      "<div><!---><a download>x --></div>\n\nSecond.",
      "<div><a download>x<!-- fake </a> --!></div>\n\nSecond.",
    ]) {
      expect(leaks(render(marked, source), "Second."), source).toBe(false);
    }
  });

  it("does not let a closer inside a cell, caption or marquee close an opener outside", () => {
    for (const source of [
      "<a download>\n\n<table><tr><td></a></td></tr></table>\n\nSecond.",
      "<div><a download>x<marquee></a></marquee></div>\n\nSecond.",
      "<div><a download>x<table><caption></a></caption></table></div>\n\nSecond.",
    ]) {
      expect(leaks(render(STOCK, source), "Second."), source).toBe(true);
      expect(leaks(render(marked, source), "Second."), source).toBe(false);
    }
  });

  it("resumes at the end tag of a raw-text element, whatever its body looks like", () => {
    // The body opens a comment that never ends; the real closer after </textarea> still counts.
    const el = render(marked, "<div><a href=\"x\">l<textarea><!-- </textarea> y</a></div>\n\nSecond.");
    expect(el.querySelector("div > a[href]")).not.toBeNull();
    expect(leaks(el, "Second.")).toBe(false);
  });

  it("keeps real pairs next to those states", () => {
    for (const [source, selector] of [
      ['<div><a href="x">l</a><textarea></a></textarea></div>', "div > a[href]"],
      ['<table><tr><td><a href="x">l</a></td></tr></table>', "td a[href]"],
      ['<div><!-- c --><a href="x">l</a></div>', "div > a[href]"],
      ['<div><a href="x"><marquee>m</marquee>l</a></div>', "div > a[href]"],
    ] as const) {
      expect(render(marked, source).querySelector(selector), source).not.toBeNull();
    }
  });
});
