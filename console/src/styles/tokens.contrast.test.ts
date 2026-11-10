// Text-contrast tokens (tokens.css "Text contrast" blocks): the ratios of --fg / --muted / --muted2
// against the three surfaces they are painted on must rise strictly Standard → Strong → Strongest
// in both themes, computed from the CSS itself so a hand edit cannot drift from the comment.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const css = readFileSync(path.resolve(__dirname, "tokens.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

type Block = { selector: string; body: string };
const blocks: Block[] = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({
  selector: m[1].trim(),
  body: m[2],
}));

function decl(body: string, name: string): string | null {
  const m = body.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{3,6})\\s*;`));
  return m ? m[1] : null;
}

function lum(hex: string): number {
  let h = hex.slice(1);
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function ratio(a: string, b: string): number {
  const [x, y] = [lum(a), lum(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

const LEVELS = ["standard", "strong", "strongest"] as const;
const THEMES = ["light", "dark"] as const;
const SURFACES = ["--bg", "--panel", "--panel2"] as const;
const TEXT = ["--fg", "--muted", "--muted2"] as const;

// Theme base block = the one whose selector list carries [data-theme="<t>"] without data-contrast.
function baseBlock(theme: string): Block {
  const b = blocks.find((x) => x.selector.includes(`[data-theme="${theme}"]`) && !x.selector.includes("data-contrast"));
  if (!b) throw new Error(`no base block for ${theme}`);
  return b;
}
function contrastBlock(level: string, theme: string): Block | undefined {
  return blocks.find((x) => x.selector.includes(`:root[data-contrast="${level}"][data-theme="${theme}"]`));
}

function palette(level: (typeof LEVELS)[number], theme: (typeof THEMES)[number]) {
  const base = baseBlock(theme);
  const over = level === "standard" ? null : contrastBlock(level, theme);
  if (level !== "standard") expect(over, `${level}/${theme} block`).toBeDefined();
  const text = (n: string) => (over && decl(over.body, n)) || decl(base.body, n)!;
  // dark's surfaces live in the :root block; the light theme's in its own.
  const surf = (n: string) => decl(base.body, n)!;
  return { text, surf };
}

describe("text contrast tokens", () => {
  for (const theme of THEMES) {
    it(`${theme}: every text token's ratio rises strictly Standard → Strong → Strongest on every surface`, () => {
      for (const t of TEXT) {
        for (const s of SURFACES) {
          const r = LEVELS.map((l) => {
            const p = palette(l, theme);
            return ratio(p.text(t), p.surf(s));
          });
          expect(r[1], `${theme} ${t} on ${s}: strong > standard`).toBeGreaterThan(r[0]);
          expect(r[2], `${theme} ${t} on ${s}: strongest > strong`).toBeGreaterThan(r[1]);
        }
      }
    });

    it(`${theme}: the fg → muted → muted2 ladder stays visible at every level`, () => {
      for (const l of LEVELS) {
        const p = palette(l, theme);
        for (const s of SURFACES) {
          expect(ratio(p.text("--fg"), p.surf(s))).toBeGreaterThan(ratio(p.text("--muted"), p.surf(s)));
          expect(ratio(p.text("--muted"), p.surf(s))).toBeGreaterThan(ratio(p.text("--muted2"), p.surf(s)));
        }
      }
    });
  }

  it("light floors from the issue hold on every surface", () => {
    const floors = { strong: [15, 7, 6], strongest: [17.5, 9, 7.5] } as const;
    for (const l of ["strong", "strongest"] as const) {
      const p = palette(l, "light");
      TEXT.forEach((t, i) => {
        for (const s of SURFACES) expect(ratio(p.text(t), p.surf(s)), `${l} ${t} on ${s}`).toBeGreaterThanOrEqual(floors[l][i]);
      });
    }
  });

  it("dark never gets worse than standard at any level (floors: muted 6+, muted2 5+ at strong)", () => {
    const p = palette("strong", "dark");
    for (const s of SURFACES) {
      expect(ratio(p.text("--muted"), p.surf(s))).toBeGreaterThanOrEqual(6);
      expect(ratio(p.text("--muted2"), p.surf(s))).toBeGreaterThanOrEqual(5);
    }
  });

  it("each override block matches the root and nested regions that re-declare data-theme", () => {
    for (const l of ["strong", "strongest"]) {
      for (const t of THEMES) {
        const b = contrastBlock(l, t)!;
        expect(b.selector).toContain(`[data-contrast="${l}"] [data-theme="${t}"]`);
      }
    }
  });
});
