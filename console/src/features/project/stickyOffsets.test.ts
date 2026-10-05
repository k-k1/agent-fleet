// The worktree header's sticky offset adds the ROOT's measured header height
// (--proj-base-head-h, published by RepoNode on each root <li>). A worktree can itself be a
// root when its base clone is gone; if the worktree rule matched it, it would add its own
// height and pin with an empty band above it. jsdom does not apply this CSS, so the rule
// text is checked instead.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = fs
  .readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "project.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");

/** Every top-level `selector { body }` rule (project.css has no at-rule nesting around these). */
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
  selectors: m[1].split(",").map((s) => s.trim().replace(/\s+/g, " ")),
  body: m[2],
}));

const ANCHOR = ".proj-tree > .proj-node > .proj-children > ";

describe("project.css sticky worktree headers", () => {
  const sticky = rules.filter((r) => /position:\s*sticky/.test(r.body));

  it("pins worktree headers only as first-level children of a root", () => {
    const wt = sticky.flatMap((r) => r.selectors).filter((s) => /\.wt\b[^,]*\.proj-node-head$/.test(s));
    expect(wt.length).toBeGreaterThan(0);
    for (const s of wt) expect(s.startsWith(ANCHOR), s).toBe(true);
  });

  it("offsets that tier by the root's measured header height", () => {
    const r = sticky.find((x) => x.selectors.some((s) => s.startsWith(ANCHOR)));
    expect(r?.body).toMatch(/top:[^;]*var\(--proj-filter-h[^;]*var\(--proj-base-head-h/);
  });

  it("keeps deeper worktree headers unpinned but in their own stacking context", () => {
    const deep = rules.find((r) => r.selectors.includes(".proj-children .proj-children .proj-node.wt > .proj-node-head"));
    expect(deep?.body).toMatch(/position:\s*relative/);
    expect(deep?.body).toMatch(/z-index:\s*0/);
  });
});
