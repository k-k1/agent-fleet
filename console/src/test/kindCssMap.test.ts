// Kind colours have one place (ADR 0124 Phase 3): styles/tokens.css maps each `.kind-<slug>` to
// --kind, and the use sites paint with var(--kind) behind the `kind` marker that
// kindColorClasses() writes. These checks keep it that way: the map covers exactly the
// registry's slugs and no component spells the class itself.
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { AGENTS } from "../agents/registry.ts";
import { kindClasses, kindColorClasses } from "../lib/sessionkind.ts";
import { SESSION_KINDS } from "../types/session.ts";

const SRC = path.resolve(__dirname, "..");
const TOKENS = path.join(SRC, "styles", "tokens.css");
const SLUGS = SESSION_KINDS.map((k) => AGENTS[k].cssClass);

function files(dir: string, re: RegExp, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) files(p, re, out);
    else if (re.test(e.name)) out.push(p);
  }
  return out;
}

const stripComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, "");

describe("kind colour map", () => {
  it("maps exactly the registry's slugs in tokens.css, each to its own token", () => {
    const css = stripComments(readFileSync(TOKENS, "utf8"));
    const mapped = [...css.matchAll(/^\.kind-([a-z]+) \{ --kind: var\(--kind-([a-z]+)\); \}$/gm)];
    expect(mapped.length, "the map in tokens.css was not found").toBeGreaterThan(0);
    for (const m of mapped) expect(m[2], `.kind-${m[1]}`).toBe(m[1]);
    expect(mapped.map((m) => m[1]).sort()).toEqual([...SLUGS].sort());
    // Each token is defined for both themes (the dark :root block and the light override).
    for (const s of SLUGS) {
      expect(css.match(new RegExp(`--kind-${s}:`, "g"))?.length ?? 0, `--kind-${s}`).toBeGreaterThanOrEqual(2);
    }
  });

  it("no component builds a kind colour class by hand", () => {
    const HAND = /(^|[\s"'`])kind-["'`]\s*\+/;
    const found = files(SRC, /\.tsx?$/)
      .filter((f) => !f.includes(".test."))
      .flatMap((f) =>
        readFileSync(f, "utf8")
          .split("\n")
          .filter((l) => HAND.test(l))
          .map((l) => `${path.relative(SRC, f)}: ${l.trim()}`),
      );
    expect(found).toEqual([]);
    expect(HAND.test(`className={"sess-kic kind-" + kindClass(k)}`)).toBe(true);
    expect(HAND.test(`className={"inbox-kind inbox-kind-" + item.kind}`)).toBe(false);
  });
});

describe("kindColorClasses", () => {
  it("marks every registered slug and nothing else", () => {
    for (const s of SLUGS) expect(kindColorClasses(s)).toBe(`kind kind-${s}`);
    expect(kindColorClasses("generic")).toBe("kind-generic");
    expect(kindColorClasses("other")).toBe("kind-other");
    expect(kindClasses("codex")).toBe("kind kind-codex");
    expect(kindClasses("Codex")).toBe("kind kind-claude"); // agentOf's own fallback, as kindClass
  });
});
