import { describe, expect, it } from "vitest";
import { marked } from "../../../lib/markdown.ts";
import { repairFullwidthTables } from "../../../lib/markdown.ts";
import { SOURCE_LINE_ATTR, insertedLineMap, tagSourceLines } from "./mdSourceLines.ts";

function render(source: string, lineOffset = 0): HTMLElement {
  const root = document.createElement("div");
  root.innerHTML = marked.parse(source, { gfm: true }) as string;
  tagSourceLines(root, source, { lineOffset });
  return root;
}

const lineOf = (el: Element | null | undefined) => el?.getAttribute(SOURCE_LINE_ATTR) ?? null;

describe("tagSourceLines", () => {
  const doc = [
    "# Title", //            1
    "", //                   2
    "First paragraph", //    3
    "still first.", //       4
    "", //                   5
    "- one", //              6
    "- two", //              7
    "  - nested", //         8
    "- three", //            9
    "", //                  10
    "```js", //             11
    "x = 1", //             12
    "```", //               13
    "", //                  14
    "| a | b |", //         15
    "|---|---|", //         16
    "| 1 | 2 |", //         17
    "", //                  18
    "<div>raw html</div>", // 19
    "", //                  20
    "## End", //            21
  ].join("\n");

  it("tags every top-level block with its 1-based source line", () => {
    const root = render(doc);
    expect(lineOf(root.querySelector("h1"))).toBe("1");
    expect(lineOf(root.querySelector(":scope > p"))).toBe("3");
    expect(lineOf(root.querySelector(":scope > ul"))).toBe("6");
    expect(lineOf(root.querySelector(":scope > pre"))).toBe("11");
    expect(lineOf(root.querySelector(":scope > table"))).toBe("15");
    expect(lineOf(root.querySelector("h2"))).toBe("21");
  });

  it("tags list items and nested lists", () => {
    const root = render(doc);
    const items = [...root.querySelectorAll(":scope > ul > li")];
    expect(items.map(lineOf)).toEqual(["6", "7", "9"]);
    expect(lineOf(root.querySelector("ul ul"))).toBe("8");
    expect(lineOf(root.querySelector("ul ul > li"))).toBe("8");
  });

  it("leaves raw HTML untagged without derailing the blocks after it", () => {
    const root = render(doc);
    expect(root.querySelector(":scope > div")?.hasAttribute(SOURCE_LINE_ATTR)).toBe(false);
    expect(lineOf(root.querySelector("h2"))).toBe("21");
  });

  it("adds the lines in front of the body (front matter)", () => {
    const root = render("# A\n\ntext", 4);
    expect(lineOf(root.querySelector("h1"))).toBe("5");
    expect(lineOf(root.querySelector("p"))).toBe("7");
  });

  it("tags every item of a long nested list, whose raw is de-indented", () => {
    const src = ["- a", "  - n1", "  - n2", "  - n3", "  - n4", "  - n5", "- b"].join("\n");
    const root = render(src);
    expect([...root.querySelectorAll("ul ul > li")].map(lineOf)).toEqual(["2", "3", "4", "5", "6"]);
    expect([...root.querySelectorAll(":scope > ul > li")].map(lineOf)).toEqual(["1", "7"]);
  });

  it("tags the blocks of a loose list item", () => {
    const src = ["- first", "", "  more of first", "", "- second"].join("\n");
    const root = render(src);
    const [first, second] = [...root.querySelectorAll(":scope > ul > li")];
    expect(lineOf(first)).toBe("1");
    expect([...first!.querySelectorAll(":scope > p")].map(lineOf)).toEqual(["1", "3"]);
    expect(lineOf(second)).toBe("5");
  });

  it("tags a blockquote once and leaves its contents alone", () => {
    const root = render(["> one", ">", "> two", "", "after"].join("\n"));
    expect(lineOf(root.querySelector("blockquote"))).toBe("1");
    expect(root.querySelector("blockquote [data-src-line]")).toBeNull();
    expect(lineOf(root.querySelector(":scope > p"))).toBe("5");
  });

  it("numbers blocks after repaired tables by the lines the user wrote", () => {
    const body = [
      "｜a｜b｜", "｜1｜2｜", "｜3｜4｜", "｜5｜6｜", // 1-4, no delimiter: one is inserted
      "",
      "｜c｜d｜", "｜7｜8｜", "｜9｜0｜", "｜x｜y｜", // 6-9, inserted again
      "",
      "end", // 11
    ].join("\n");
    const repair = repairFullwidthTables(body)!;
    expect(repair.inserted).toHaveLength(2);
    const root = document.createElement("div");
    root.innerHTML = marked.parse(repair.body, { gfm: true }) as string;
    tagSourceLines(root, repair.body, { mapLine: insertedLineMap(repair.inserted) });
    expect([...root.querySelectorAll("table")].map(lineOf)).toEqual(["1", "6"]);
    expect(lineOf(root.querySelector(":scope > p"))).toBe("11");
  });
});
