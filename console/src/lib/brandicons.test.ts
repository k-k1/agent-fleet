// The mask lives in CSS (styles/brandicons.css) while the inventory comes from the asset
// folder (lib/brandicons), so the two can drift in either direction — and both failures are
// silent in the browser: a missing rule renders a blank gap where a logo should be, an
// orphan rule is dead weight nothing points at. This is the only thing that notices.
import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { BRAND_SETS, agentBrandClass } from "./brandicons.ts";

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

describe("brand icons", () => {
  it("has a CSS mask rule for every vendored asset, and no rule without one", () => {
    const css = fs.readFileSync(path.join(SRC, "styles/brandicons.css"), "utf8");
    const ruled = new Set([...css.matchAll(/\.bi-(\w+)-([\w-]+)\s*\{/g)].map((m) => `${m[1]}s/${m[2]}`));

    const vendored = new Set<string>();
    for (const [set, keys] of Object.entries(BRAND_SETS)) {
      for (const key of keys) vendored.add(`${set}/${key}`);
    }

    // Positive control: a green result must mean both sides were actually populated, not
    // that the glob returned nothing and the empty sets matched.
    expect(vendored.size).toBeGreaterThan(0);
    expect(vendored.has("agents/claude")).toBe(true);

    expect([...vendored].filter((k) => !ruled.has(k)).sort()).toEqual([]);
    expect([...ruled].filter((k) => !vendored.has(k)).sort()).toEqual([]);
  });

  it("names every agent kind's icon that the registry asks for", async () => {
    const { AGENTS } = await import("../agents/registry.ts");
    const missing = Object.values(AGENTS)
      .map((a) => (a as { icon: string }).icon)
      .filter((icon) => icon.startsWith("brand:"))
      .filter((icon) => agentBrandClass(icon.slice("brand:".length)) === null);
    expect(missing).toEqual([]);
  });
});
