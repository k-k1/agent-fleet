// Guards the invariants of the split catalogue (ADR 0067 decision 4).
//
// The point of the split is that each front-end session only appends to its own domain file and
// so never collides. Three conditions make that hold, and breaking any of them still leaves the
// app more or less working, so only a test can stop it:
//
//  1. The same key must not exist in two domain files. Composition is a spread, so the later one
//     silently wins: two sessions adding the same key in different files lose one wording and
//     stay green.
//  2. One key prefix ("chat" / "settings" …) belongs to exactly one file. Without that, "my
//     domain's file" is undefined and everyone edits the same file again, which is the state the
//     split removed.
//  3. Every domain present in the directory must also be in the composition. This failure only
//     became possible with the split, and it slips through every gate (see below).
//
// That ja and en have matching file structures is checked here too; key coverage itself is
// covered by tsc and i18n.test.ts.
import { describe, it, expect } from "vitest";
import { ja } from "./locales/ja.ts";
import { en } from "./locales/en.ts";

type Catalog = Record<string, string>;
const jaModules = import.meta.glob<{ [k: string]: Catalog }>("./locales/ja/*.ts", { eager: true });
const enModules = import.meta.glob<{ [k: string]: Catalog }>("./locales/en/*.ts", { eager: true });

// Reads the contents under the convention of one named export (the domain name) per file.
function catalogsOf(mods: Record<string, { [k: string]: Catalog }>): Map<string, Catalog> {
  const out = new Map<string, Catalog>();
  for (const [path, mod] of Object.entries(mods)) {
    const values = Object.values(mod).filter((v) => v && typeof v === "object");
    expect(values, `${path} must have exactly one named export`).toHaveLength(1);
    out.set(path.replace(/^.*\//, "").replace(/\.ts$/, ""), values[0] as Catalog);
  }
  return out;
}

const jaCatalogs = catalogsOf(jaModules);
const enCatalogs = catalogsOf(enModules);

describe("i18n catalogue split", () => {
  it("has more than one domain file, with matching structure in ja and en", () => {
    expect(jaCatalogs.size).toBeGreaterThan(1);
    expect([...enCatalogs.keys()].sort()).toEqual([...jaCatalogs.keys()].sort());
  });

  // Without this check, adding a new domain while forgetting the import and spread in ja.ts /
  // en.ts passes every gate green (measured): this file's glob reads the directory directly so
  // the domain looks present, tsc does not complain about an unused file, and i18n.test.ts
  // compares ja and en after composition, so forgetting both sides shows no difference. All t()
  // does then is print the raw key on screen. Adding a domain means two new files plus two
  // composition files, so forgetting the latter two is the ordinary way to slip.
  //
  // The reverse direction (a key only in the composition) is checked at the same time: writing
  // keys directly into ja.ts / en.ts brings back the single file everyone edits.
  it.each([
    ["ja", jaCatalogs, ja as Catalog],
    ["en", enCatalogs, en as Catalog],
    // Use exactly one %s in the title: it.each fills the remaining arguments in order, so a
    // second one expands the whole catalogue Map (4,112 keys) into the test name.
  ] as const)("%s: domain files and the composed file hold the same set of keys", (locale, catalogs, composed) => {
    const notComposed: string[] = [];
    for (const [domain, cat] of catalogs) {
      const miss = Object.keys(cat).filter((k) => !(k in composed));
      // Report per file: a whole domain missing and a few keys missing are fixed in different
      // places (the import and spread, versus the domain file itself).
      if (miss.length > 0) {
        notComposed.push(
          `${locale}/${domain}.ts: ${miss.length}/${Object.keys(cat).length} keys are absent from locales/${locale}.ts` +
            ` (e.g. ${miss.slice(0, 3).join(", ")}) - check the import and ...${domain} were not forgotten`,
        );
      }
    }
    expect(notComposed).toEqual([]);

    const fromDomains = new Set<string>();
    for (const cat of catalogs.values()) for (const k of Object.keys(cat)) fromDomains.add(k);
    const onlyComposed = Object.keys(composed).filter((k) => !fromDomains.has(k));
    expect(onlyComposed, `keys written directly in locales/${locale}.ts (move them to a domain file)`).toEqual([]);
  });

  it.each(["ja", "en"] as const)("%s: no key exists in two files", (locale) => {
    const owner = new Map<string, string>();
    const dup: string[] = [];
    for (const [domain, cat] of locale === "ja" ? jaCatalogs : enCatalogs) {
      for (const key of Object.keys(cat)) {
        const prev = owner.get(key);
        if (prev) dup.push(`${key}: ${prev} and ${domain}`);
        else owner.set(key, domain);
      }
    }
    expect(dup).toEqual([]);
  });

  it("gives each key prefix exactly one file", () => {
    const owner = new Map<string, string>();
    const split: string[] = [];
    for (const [domain, cat] of jaCatalogs) {
      for (const key of Object.keys(cat)) {
        const prefix = key.split(".")[0];
        const prev = owner.get(prefix);
        if (prev === undefined) owner.set(prefix, domain);
        else if (prev !== domain) split.push(`${prefix}: ${prev} and ${domain} (key ${key})`);
      }
    }
    expect([...new Set(split)]).toEqual([]);
  });

  it("keeps the same keys in the same file for ja and en (neither side sits in another domain)", () => {
    for (const [domain, jaCat] of jaCatalogs) {
      const enCat = enCatalogs.get(domain);
      expect(enCat, `en/${domain}.ts is missing`).toBeDefined();
      expect(Object.keys(enCat as Catalog).sort()).toEqual(Object.keys(jaCat).sort());
    }
  });
});
