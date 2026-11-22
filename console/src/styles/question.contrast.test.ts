// Answered question card: the option descriptions must follow the Text contrast setting. They did not
// while the card dimmed text with stacked `opacity` (0.9 × 0.55 × 0.68 on an unpicked description), which
// no token can lift. Computed from the CSS itself, the way tokens.contrast.test.ts does.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const strip = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const tokens = strip(readFileSync(path.resolve(__dirname, "tokens.css"), "utf8"));
const question = strip(readFileSync(path.resolve(__dirname, "../features/mirror/parts/question.css"), "utf8"));

type Block = { selector: string; body: string };
const parse = (css: string): Block[] => [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ selector: m[1].trim(), body: m[2] }));
const tokenBlocks = parse(tokens);
const questionBlocks = parse(question);

const decl = (body: string, name: string) => body.match(new RegExp(`(?:^|[;\\s])${name}:\\s*(#[0-9a-fA-F]{3,6})\\s*;`))?.[1] ?? null;

type RGB = [number, number, number];
function rgb(hex: string): RGB {
  let h = hex.slice(1);
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as RGB;
}
// color-mix(in srgb, a pct%, b)
const mix = (a: RGB, pct: number, b: RGB): RGB => a.map((v, i) => v * pct + b[i] * (1 - pct)) as RGB;
function lum(c: RGB): number {
  const [r, g, b] = c.map((v) => v / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
const ratio = (a: RGB, b: RGB) => (Math.max(lum(a), lum(b)) + 0.05) / (Math.min(lum(a), lum(b)) + 0.05);

const LEVELS = ["standard", "strong", "strongest"] as const;
const THEMES = ["light", "dark"] as const;

function palette(level: (typeof LEVELS)[number], theme: (typeof THEMES)[number]) {
  const base = tokenBlocks.find((x) => x.selector.includes(`[data-theme="${theme}"]`) && !x.selector.includes("data-contrast"))!;
  const over = level === "standard" ? undefined : tokenBlocks.find((x) => x.selector.includes(`:root[data-contrast="${level}"][data-theme="${theme}"]`));
  const get = (n: string) => rgb((over && decl(over.body, n)) || decl(base.body, n) || decl(tokenBlocks.find((x) => x.selector.trim() === ":root")!.body, n)!);
  return get;
}

function rule(selector: string): string {
  const b = questionBlocks.find((x) => x.selector.split(",").map((s) => s.trim()).includes(selector));
  if (!b) throw new Error(`no rule ${selector}`);
  return b.body;
}

describe("answered question card text", () => {
  it("dims no text with opacity", () => {
    for (const sel of [".mt-question.answered", ".mq-opt:disabled", ".mq-opt-desc", ".mq-opt.selected", ".mq-mark", ".mq-free-tag", ".mq-freetext:disabled", ".mq-freetext.inactive"]) {
      expect(rule(sel), sel).not.toMatch(/opacity\s*:\s*(?!1\b)/);
    }
  });

  it("colours descriptions with tokens: --muted, --fg once picked", () => {
    expect(rule(".mq-opt-desc")).toMatch(/color:\s*var\(--muted\)/);
    expect(rule(".mq-opt.selected .mq-opt-desc")).toMatch(/color:\s*var\(--fg\)/);
    expect(rule(".mt-question.answered")).toMatch(/var\(--fg\) 1%/);
  });

  for (const theme of THEMES) {
    for (const level of LEVELS) {
      it(`${theme}/${level}: picked and unpicked descriptions reach 4.5:1 on the answered card`, () => {
        const p = palette(level, theme);
        const card = mix(p("--fg"), 0.01, p("--bg"));
        const picked = mix(p("--accent"), 0.14, card); // accent-tinted (--chat-accent defaults to --accent)
        expect(ratio(p("--muted"), card), "unpicked").toBeGreaterThanOrEqual(4.5);
        expect(ratio(p("--fg"), picked), "picked").toBeGreaterThanOrEqual(4.5);
      });
    }
  }
});
