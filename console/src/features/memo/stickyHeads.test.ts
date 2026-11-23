// jsdom applies no CSS, so the pinned repo / category rows are checked on the rule text:
// repo row under the section header, category row under the repo row's measured height,
// z-index descending so an inner row slides under an outer one, both with an opaque base.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = fs
  .readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "memo.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");
const body = (sel: string) =>
  [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((m) => m[1].trim() === sel)?.[2] ?? "";

describe("memo.css pinned headers", () => {
  it("pins the repo row under the section header", () => {
    const b = body(".memo-repo-head");
    expect(b).toMatch(/position:\s*sticky/);
    expect(b).toMatch(/top:\s*var\(--sec-head-h/);
    expect(b).toMatch(/z-index:\s*2/);
    expect(b).toMatch(/background:\s*var\(--leftpane-bg/);
  });
  it("pins the category row under the repo row, defaulting its height to 0", () => {
    const b = body(".memo-cat-head");
    expect(b).toMatch(/position:\s*sticky/);
    expect(b).toMatch(/top:\s*calc\(var\(--sec-head-h[^;]*var\(--memo-repo-head-h,\s*0px\)/);
    expect(b).toMatch(/z-index:\s*1/);
    expect(b).toMatch(/background-color:\s*var\(--leftpane-bg/);
  });
  // The settings card once reused the sidebar band's names (.memo-repo / .memo-repo-head), so
  // inside the dialog its heading stuck over its own hint. css-dup.mjs only compares files, so
  // a same-file clash like this is pinned here.
  it("keeps the repository settings card out of the sidebar band's class names", () => {
    const tsx = fs.readFileSync(
      path.join(path.dirname(fileURLToPath(import.meta.url)), "MemoRepoSettingsCard.tsx"),
      "utf8",
    );
    expect(tsx).not.toMatch(/memo-repo(?![\w-]*set)(?=[\s"-])/);
    expect(tsx).toMatch(/memo-reposet-head/);
    expect(body(".memo-reposet-head,\n.memo-reposet-foot")).not.toMatch(/sticky/);
  });
});
