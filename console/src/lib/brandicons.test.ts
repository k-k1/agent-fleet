// Three things have to name the same icons and none of them can see the others: the asset
// folders, the hand-kept key lists in lib/brandicons (hand-kept because ui/ must stay free of
// Vite-only syntax — see the comment there), and the CSS mask rules. Every mismatch is silent
// in the browser: a key with no rule renders a blank gap, a key with no asset renders a blank
// gap, an orphan rule is dead weight nothing points at. This is the only thing that notices.
import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { BRAND_SETS, BRAND_CLASS, brandClass } from "./brandicons.ts";

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.join(SRC, "assets/brandicons");

describe("brand icons", () => {
  it("names the same icons in the asset folders, the key lists and the CSS", () => {
    const onDisk = new Set<string>();
    for (const set of fs.readdirSync(ROOT, { withFileTypes: true })) {
      if (!set.isDirectory()) continue;
      for (const f of fs.readdirSync(path.join(ROOT, set.name))) {
        if (f.endsWith(".svg")) onDisk.add(`${set.name}/${f.slice(0, -4)}`);
      }
    }

    const listed = new Set<string>();
    for (const [dir, keys] of Object.entries(BRAND_SETS)) {
      for (const key of keys) listed.add(`${dir}/${key}`);
    }

    // Resolve each rule back through BRAND_CLASS rather than guessing the folder from the
    // class name, so renaming a set cannot make this quietly compare nothing.
    const css = fs.readFileSync(path.join(SRC, "styles/brandicons.css"), "utf8");
    const byPrefix = Object.entries(BRAND_CLASS).map(([dir, cls]) => [cls, dir] as const);
    const ruled = new Set<string>();
    for (const m of css.matchAll(/\.(bi-[\w-]+?)\s*\{/g)) {
      const hit = byPrefix.find(([cls]) => m[1].startsWith(cls));
      ruled.add(hit ? `${hit[1]}/${m[1].slice(hit[0].length)}` : `UNKNOWN-SET/${m[1]}`);
    }

    // Positive control: a green result must mean all three sides were actually populated, not
    // that a read came back empty and three empty sets matched each other.
    for (const probe of ["agents/claude", "services/github"]) {
      expect(onDisk.has(probe)).toBe(true);
      expect(listed.has(probe)).toBe(true);
      expect(ruled.has(probe)).toBe(true);
    }

    expect([...onDisk].sort()).toEqual([...listed].sort());
    expect([...onDisk].sort()).toEqual([...ruled].sort());
  });

  it("keeps the keys unique across sets, so one flat brand: namespace stays unambiguous", () => {
    const seen = new Map<string, string>();
    const dupes: string[] = [];
    for (const [dir, keys] of Object.entries(BRAND_SETS)) {
      for (const key of keys) {
        if (seen.has(key)) dupes.push(`${key} (${seen.get(key)} + ${dir})`);
        seen.set(key, dir);
      }
    }
    expect(dupes).toEqual([]);
  });

  it("names every agent kind's icon that the registry asks for", async () => {
    const { AGENTS } = await import("../agents/registry.ts");
    const missing = Object.values(AGENTS)
      .map((a) => (a as { icon: string }).icon)
      .filter((icon) => icon.startsWith("brand:"))
      .filter((icon) => brandClass(icon.slice("brand:".length)) === null);
    expect(missing).toEqual([]);
  });

  it("has a mark for every settings card that is not deliberately a monogram", async () => {
    // The service icons are named after the settings card's provider id, so a card added
    // without an icon is only visible as "that one still shows two letters". The exceptions
    // are listed, which is what makes leaving one out a decision rather than an oversight.
    const MONOGRAM_ONLY = ["svn", "internal"];
    const cards = new Set<string>();
    const dir = path.join(SRC, "features/settings");
    const walk = (d: string) => {
      for (const e of fs.readdirSync(d, { withFileTypes: true })) {
        const p = path.join(d, e.name);
        if (e.isDirectory()) walk(p);
        else if (e.name.endsWith(".tsx") && !e.name.includes(".test.")) {
          const src = fs.readFileSync(p, "utf8");
          for (const m of src.matchAll(/<ProviderCard[^>]*?\bid="([\w-]+)"/gs)) cards.add(m[1]);
        }
      }
    };
    walk(dir);

    expect(cards.size).toBeGreaterThan(10); // positive control: the walk really found cards
    const { AGENTS } = await import("../agents/registry.ts");
    const agentIds = new Set(Object.values(AGENTS).map((a) => a.id as string));
    const missing = [...cards]
      .filter((id) => !MONOGRAM_ONLY.includes(id) && !agentIds.has(id))
      .filter((id) => brandClass(id) === null);
    expect(missing.sort()).toEqual([]);
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
