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

// Percent of `color-mix(in srgb, <x> N%, <y>)` declared as `background` in the rule.
function mixPct(selector: string): number {
  const m = rule(selector).match(/background:\s*color-mix\(in srgb,\s*var\(--[\w-]+\)\s+(\d+(?:\.\d+)?)%,\s*var\(--bg\)\)/);
  if (!m) throw new Error(`no color-mix background in ${selector}`);
  return Number(m[1]) / 100;
}

describe("answered question card text", () => {
  it("dims no text with opacity", () => {
    for (const b of questionBlocks) expect(b.body, b.selector).not.toMatch(/(?<![\w-])opacity\s*:\s*(?!1\s*;)/);
  });

  it("an inert unpicked option stays distinguishable from a live one without fading text", () => {
    const body = rule(".mq-opt:disabled:not(.mq-opt--selected):not(.checked)");
    expect(body).toMatch(/border-color:/);
    expect(body).toMatch(/background:\s*transparent/);
    expect(rule(".mq-opt:disabled:not(.mq-opt--selected):not(.checked) .mq-mark")).toMatch(/color:\s*var\(--muted\)/);
  });

  it("an inert checked option differs from a live checked one by border and tint", () => {
    const body = rule(".mq-opt.checked:disabled");
    expect(body).toMatch(/border-color:/);
    expect(body).toMatch(/background:/);
    expect(body).not.toMatch(/(?<![\w-])color:/);
    expect(mixPct(".mq-opt.checked:disabled")).toBeLessThan(mixPct(".mq-opt.checked"));
  });

  it("colours answered descriptions with tokens: --muted, --fg once picked", () => {
    expect(rule(".mt-question.answered .mq-opt-desc")).toMatch(/color:\s*var\(--muted\)/);
    expect(rule(".mt-question.answered .mq-opt.mq-opt--selected .mq-opt-desc")).toMatch(/color:\s*var\(--fg\)/);
  });

  // An unpicked option is transparent over the card; the picked one tints --bg with the accent, which the
  // user chooses (--chat-accent), so check the extremes as well as the default.
  const ACCENTS: Record<string, RGB | null> = { default: null, black: [0, 0, 0], white: [255, 255, 255], yellow: [255, 255, 0] };

  for (const theme of THEMES) {
    for (const level of LEVELS) {
      it(`${theme}/${level}: picked and unpicked descriptions reach 4.5:1 on the answered card`, () => {
        const p = palette(level, theme);
        const card = mix(p("--fg"), mixPct(".mt-question.answered"), p("--bg"));
        expect(rule(".mq-opt:disabled:not(.mq-opt--selected):not(.checked)")).toMatch(/background:\s*transparent/);
        expect(ratio(p("--muted"), card), "unpicked").toBeGreaterThanOrEqual(4.5);
        for (const [name, a] of Object.entries(ACCENTS)) {
          const picked = mix(a ?? p("--accent"), mixPct(".mq-opt.mq-opt--selected"), p("--bg"));
          expect(ratio(p("--fg"), picked), `picked, accent ${name}`).toBeGreaterThanOrEqual(4.5);
        }
      });
    }
  }
});

// The generic disabled-button fade in the mirror shell reached the answered card's options (they are
// disabled <button>s) and dimmed their text to about 2:1 in the light theme; the cascade is invisible to
// the dom tests, so pin the exemption in the source. It must be wrapped in :where(): a bare :not(.mq-opt)
// raises the rule's specificity and lets it beat every `.x:disabled` opacity elsewhere (pending.css,
// composer.css, marks.css).
describe("generic disabled-button opacity", () => {
  it("skips question options in every shell without changing specificity", () => {
    const shell = strip(readFileSync(path.resolve(__dirname, "../features/mirror/parts/shell.css"), "utf8"));
    const fading = parse(shell).filter((b) => /opacity\s*:\s*(?!1\s*;)/.test(b.body) && /button:disabled/.test(b.selector));
    expect(fading.length).toBeGreaterThan(0);
    for (const b of fading) {
      // Whole selector, so a scope that adds specificity (`:is()` for `:where()`) is caught too.
      expect(b.selector, b.selector).toMatch(/^:where\([^()]*\) button:disabled:where\(:not\(\.mq-opt\)\)$/);
    }
  });
});
