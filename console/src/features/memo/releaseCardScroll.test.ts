// jsdom applies no CSS, so the release card's scroll contract is checked on the rule text:
// .memo-release-items is the only scroller (it keeps the approve button on screen), and the
// item text must not scroll on its own, or two scrollbars sit side by side mid-card.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = fs
  .readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "memo.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");
const body = (sel: string) =>
  [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((m) => m[1].trim() === sel)?.[2] ?? "";

describe("memo.css release card scrolling", () => {
  it("scrolls the item list, not each item text", () => {
    const items = body(".memo-release-items");
    expect(items).toMatch(/max-height:\s*40vh/);
    expect(items).toMatch(/overflow-y:\s*auto/);
    const text = body(".memo-release-text");
    expect(text).not.toBe("");
    expect(text).not.toMatch(/max-height|overflow(-[xy])?\s*:/);
  });
});
