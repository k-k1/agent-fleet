// Regression pin for the kind table (#2933). SESSION_KINDS, FALLBACK_KIND_FACTS and the kind
// colour tokens used to be typed out by hand; they are now derived from one table. These are the
// literals they replaced, so deriving them cannot change what a user sees for a kind that already
// exists. Only the kinds listed here are constrained: a kind added later lands where the table
// puts it without editing this file.
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { FALLBACK_KIND_FACTS } from "./kindCatalog.ts";
import { expandKindTokens, kindTokenCss } from "./kindTable.ts";
import { SESSION_KINDS } from "../types/session.ts";

const ORDER = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse", "shell", "ssm"];

const FACTS: Record<string, [managedOnly: boolean, managedDefault: boolean, mcpClient: string]> = {
  claude: [false, false, "yes"],
  codex: [false, true, "yes"],
  cursor: [false, true, "yes"],
  copilot: [false, true, "yes"],
  kiro: [false, true, "yes"],
  agy: [false, false, "yes"],
  opencode: [false, true, "yes"],
  lcpp: [true, true, "yes"],
  muse: [true, true, "yes"],
  shell: [false, false, "no"],
  ssm: [false, false, "no"],
};

const HUES: Record<string, [dark: string, light: string]> = {
  claude: ["#e0a45e", "#8f4f13"],
  codex: ["#4ec97a", "#126b3b"],
  cursor: ["#d96ba1", "#a02a63"],
  agy: ["#4285f4", "#0f5ec4"],
  kiro: ["#a371f7", "#6b3ac9"],
  copilot: ["#7d8590", "#30363d"],
  opencode: ["#aab4be", "#575e68"],
  lcpp: ["#d9c952", "#5c5400"],
  muse: ["#dbabe6", "#6b2d7e"],
  shell: ["#46c9d0", "#0f676d"],
  ssm: ["#6d8bf5", "#2c39a3"],
};

// The stylesheet as the build sees it: the markers expanded from the table, comments dropped.
const tokensCss = () =>
  expandKindTokens(readFileSync(path.resolve(__dirname, "..", "styles", "tokens.css"), "utf8")).replace(/\/\*[\s\S]*?\*\//g, "");

describe("the kind table equals the literals it replaced", () => {
  it("keeps SESSION_KINDS order", () => {
    expect(SESSION_KINDS.filter((k) => ORDER.includes(k))).toEqual(ORDER);
  });

  it.each(Object.entries(FACTS))("keeps the fallback facts of %s", (kind, [managedOnly, managedDefault, mcpClient]) => {
    expect(FALLBACK_KIND_FACTS[kind as keyof typeof FALLBACK_KIND_FACTS]).toEqual({ managedOnly, managedDefault, mcpClient });
  });

  it.each(Object.entries(HUES))("keeps the colour tokens of %s in both themes", (kind, [dark, light]) => {
    const css = tokensCss();
    const all = [...css.matchAll(new RegExp(`--kind-${kind}:\\s*(#[0-9a-f]{6});`, "g"))].map((m) => m[1]);
    expect(all).toEqual([dark, light]); // dark :root block first, light override after
    expect(css).toContain(`.kind-${kind} { --kind: var(--kind-${kind}); }`);
  });
});

describe("the kind table drives the rest", () => {
  it("has exactly one registry descriptor per row, in the same order", async () => {
    const { AGENTS } = await import("./registry.ts");
    const { KIND_TABLE } = await import("./kindTable.ts");
    expect(Object.keys(AGENTS).sort()).toEqual(KIND_TABLE.map((r) => r.id).sort());
    expect([...SESSION_KINDS]).toEqual(KIND_TABLE.map((r) => r.id));
  });

  it("expands a row added to the table into a token pair and a map line", () => {
    expect(kindTokenCss("dark")).toContain("--kind-claude: #e0a45e;");
    expect(kindTokenCss("light")).toContain("--kind-claude: #8f4f13;");
    expect(kindTokenCss("map")).toContain(".kind-claude { --kind: var(--kind-claude); }");
    expect(expandKindTokens("a /* @af-kind-tokens map */ b")).toContain(".kind-ssm { --kind: var(--kind-ssm); }");
    expect(expandKindTokens("no markers")).toBe("no markers");
  });
});

describe("terminal background tints", () => {
  it("keep the base colour of the kinds that had one", async () => {
    const { termBackground } = await import("../lib/termcolor.ts");
    // TERM_BASE blended 13% toward the kind's dark hue, as the hand-kept KIND_BASE table did.
    for (const [kind, [dark]] of Object.entries(HUES)) {
      if (kind === "lcpp" || kind === "muse") continue; // no base before; managed-only, no pane
      expect(termBackground(kind), kind).toBe(termBackground(undefined, dark));
    }
  });
});
