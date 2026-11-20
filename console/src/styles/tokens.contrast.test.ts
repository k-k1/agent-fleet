// Text-contrast tokens (tokens.css "Text contrast" blocks): the ratios of --fg / --muted / --muted2
// against the three surfaces they are painted on must rise strictly Standard → Strong → Strongest
// in both themes, computed from the CSS itself so a hand edit cannot drift from the comment.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { SURFACE_COLORS } from "../lib/settings/options.ts";

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

// The activity bar badge paints --on-accent / --on-warn over --accent / --warn. The light theme's
// fills are dark, so a fixed dark text colour there reads as a smudge.
describe("activity bar badge text", () => {
  for (const theme of THEMES) {
    for (const [on, fill] of [["--on-accent", "--accent"], ["--on-warn", "--warn"]] as const) {
      it(`${theme}: ${on} on ${fill} clears 4.5:1`, () => {
        const base = baseBlock(theme);
        const pick = (n: string) => decl(base.body, n)!;
        expect(ratio(pick(on), pick(fill))).toBeGreaterThanOrEqual(4.5);
      });
    }
  }
});

// The pair test above proves the tokens; this proves the badge actually uses them.
describe("activity bar badge wiring", () => {
  const appCss = readFileSync(path.resolve(__dirname, "../app/app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel: string) => appCss.match(new RegExp(`${sel.replace(/[.]/g, "\\.")}\\s*\\{([^}]*)\\}`))![1];
  it("base badge text is --on-accent, tone-waiting text is --on-warn", () => {
    expect(rule(".actbar-badge")).toMatch(/color:\s*var\(--on-accent\)/);
    expect(rule(".actbar-badge.tone-waiting")).toMatch(/color:\s*var\(--on-warn\)/);
  });
});

// Light segmented-control selection: --sel-bg = accent mixed into --bg, --sel-fg = accent mixed
// into --fg (the knobs are the light block's --sel-*-mix). For every top-bar accent the fill
// must come out darker than the unselected cells (--panel2) — at a 16% mix slate and graphite
// were as light as the cells and the selection vanished — and the label must keep 4.5:1.
describe("light segment selection", () => {
  const base = baseBlock("light");
  const pct = (n: string) => Number(base.body.match(new RegExp(`${n}:\\s*(\\d+)%`))![1]) / 100;
  const hex = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const mix = (a: string, b: string, p: number) =>
    "#" + hex(a).map((v, i) => Math.round(v * p + hex(b)[i] * (1 - p)).toString(16).padStart(2, "0")).join("");
  const accents: [string, string][] = [
    ["default", decl(base.body, "--topbar-accent")!],
    ...SURFACE_COLORS.filter((c) => c.accent).map((c): [string, string] => [c.id, c.accent!]),
  ];
  for (const [id, accent] of accents) {
    it(`${id}: the fill is darker than --panel2 and the label clears 4.5:1 on it`, () => {
      const fill = mix(accent, decl(base.body, "--bg")!, pct("--sel-bg-mix"));
      const label = mix(accent, decl(base.body, "--fg")!, pct("--sel-fg-mix"));
      expect(lum(fill)).toBeLessThan(lum(decl(base.body, "--panel2")!));
      expect(ratio(label, fill)).toBeGreaterThanOrEqual(4.5);
    });
  }
});

// The ratios above hold only if the tokens are actually derived from those knobs and the outline
// is actually painted: pin the formulas in every block that declares them, the per-theme ring
// widths, and the rules in ui.css that draw the ring (the big grid has its own, which must keep
// the cell separators).
describe("segment selection wiring", () => {
  const BG = "color-mix(in srgb, var(--sel-accent) var(--sel-bg-mix), var(--bg))";
  const FG = "color-mix(in srgb, var(--sel-accent) var(--sel-fg-mix), var(--fg))";
  const value = (body: string, n: string) => body.match(new RegExp(`${n}:\\s*([^;]+);`))?.[1].trim();
  const scope = blocks.find((b) => b.selector === ".sel-scope")!;
  for (const [name, b] of [["light", baseBlock("light")], ["dark", baseBlock("dark")], [".sel-scope", scope]] as const) {
    it(`${name}: --sel-bg / --sel-fg use the mix knobs`, () => {
      expect(value(b.body, "--sel-bg")).toBe(BG);
      expect(value(b.body, "--sel-fg")).toBe(FG);
    });
  }
  it("light draws a ring, dark none", () => {
    expect(parseFloat(value(baseBlock("light").body, "--sel-ring")!)).toBeGreaterThanOrEqual(1);
    expect(parseFloat(value(baseBlock("dark").body, "--sel-ring")!)).toBe(0);
  });
  it("ui.css paints the ring on .seg-btn.active and on the big grid's active cell", () => {
    const uiCss = readFileSync(path.resolve(__dirname, "../ui/ui.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    const rule = (sel: string) =>
      [...uiCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) => m[1].trim() === sel).map((m) => m[2]).join(";");
    const ring = /inset 0 0 0 var\(--sel-ring\) var\(--sel-fg\)/;
    expect(rule(".seg-btn.active")).toMatch(ring);
    const big = rule(".ui-seg.big .seg-btn.active");
    expect(big).toMatch(ring);
    expect(big).toMatch(/inset -1px 0 0 var\(--btn-border\)/);
  });
  it("the move dialog's selected quota-left card keeps its green ring next to the selection ring", () => {
    const sessCss = readFileSync(path.resolve(__dirname, "../features/sessions/sessions.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    const both = [...sessCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((m) => m[1].trim() === ".move-form .ui-seg .seg-btn.quota-left.active")?.[2] ?? "";
    expect(both).toMatch(/var\(--ok/);
    expect(both).toMatch(/var\(--sel-ring\)\) var\(--sel-fg\)/);
  });
});
