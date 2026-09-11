// Three things have to name the same icons and none of them can see the others: the asset
// folder, the hand-kept key list in lib/brandicons (hand-kept because ui/ must stay free of
// Vite-only syntax — see the comment there), and the CSS mask rules. Every mismatch is silent
// in the browser: a key with no rule renders a blank gap, a key with no asset renders a blank
// gap, an orphan rule is dead weight nothing points at. This is the only thing that notices.
import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { BRAND_SETS, agentBrandClass } from "./brandicons.ts";

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

describe("brand icons", () => {
  it("names the same icons in the asset folder, the key list and the CSS", () => {
    const onDisk = new Set<string>();
    const root = path.join(SRC, "assets/brandicons");
    for (const set of fs.readdirSync(root, { withFileTypes: true })) {
      if (!set.isDirectory()) continue;
      for (const f of fs.readdirSync(path.join(root, set.name))) {
        if (f.endsWith(".svg")) onDisk.add(`${set.name}/${f.slice(0, -4)}`);
      }
    }

    const listed = new Set<string>();
    for (const [set, keys] of Object.entries(BRAND_SETS)) {
      for (const key of keys) listed.add(`${set}/${key}`);
    }

    const css = fs.readFileSync(path.join(SRC, "styles/brandicons.css"), "utf8");
    const ruled = new Set([...css.matchAll(/\.bi-(\w+)-([\w-]+)\s*\{/g)].map((m) => `${m[1]}s/${m[2]}`));

    // Positive control: a green result must mean all three sides were actually populated, not
    // that a read came back empty and three empty sets matched each other.
    expect(onDisk.has("agents/claude")).toBe(true);
    expect(listed.has("agents/claude")).toBe(true);
    expect(ruled.has("agents/claude")).toBe(true);

    expect([...onDisk].sort()).toEqual([...listed].sort());
    expect([...onDisk].sort()).toEqual([...ruled].sort());
  });

  it("names every agent kind's icon that the registry asks for", async () => {
    const { AGENTS } = await import("../agents/registry.ts");
    const missing = Object.values(AGENTS)
      .map((a) => (a as { icon: string }).icon)
      .filter((icon) => icon.startsWith("brand:"))
      .filter((icon) => agentBrandClass(icon.slice("brand:".length)) === null);
    expect(missing).toEqual([]);
  });

  it("keeps ui/Icon.tsx free of Vite-only syntax, which the esbuild harnesses cannot bundle", () => {
    // scripts/pdf, scripts/doc and scripts/drawio bundle the components they render with
    // esbuild. import.meta.glob survives untransformed there and throws at module init, taking
    // the whole bundle down — a failure that looks like "the view did not render", not like an
    // icon problem. Guard the two files ui/Icon.tsx pulls in.
    for (const rel of ["ui/Icon.tsx", "lib/brandicons.ts"]) {
      expect(fs.readFileSync(path.join(SRC, rel), "utf8")).not.toMatch(/import\.meta\.glob\s*[(<]/);
    }
  });
});
