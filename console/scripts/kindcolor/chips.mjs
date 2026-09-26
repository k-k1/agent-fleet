// Render every kind chip next to the semantic swatches, both themes, from the REAL tokens.css.
//
// The tokens.css comment for each new kind claims "confirmed by rendering all N chips next to the
// real diff/--err/--warn/--ok swatches headless, both themes". This is that render, kept as a
// script instead of a one-off so the twelfth kind does not have to rebuild it — and so the claim
// can be re-checked after a palette change.
//
//   node console/scripts/kindcolor/chips.mjs [--out /tmp/kindchips]
//
// It reads the token values out of styles/tokens.css rather than restating them: a chip drawn
// from a hardcoded copy would keep looking right after somebody edited the stylesheet, which is
// the one thing this is supposed to catch.
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");
const OUT = (() => {
  const i = process.argv.indexOf("--out");
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : "/tmp/kindchips";
})();

// Comments are stripped BEFORE anything is parsed, and that is not tidiness. The tokens.css
// comments explain each hue by naming the surfaces it was measured against — "contrast against
// --bg/--panel/--bar/--active-bg: 7.11 dark" — and a `--name: value;` regexp happily starts
// matching inside that prose, then runs its value up to the next real semicolon and eats the
// declaration that follows. Measured here: --kind-lcpp vanished from the parse, because the
// comment above it ends in exactly that phrase. The count assertion below is what noticed.
const css = fs
  .readFileSync(path.join(ROOT, "console/src/styles/tokens.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");

// The two theme blocks. `:root` is dark; `[data-theme="light"]` overrides it.
function block(selector) {
  const i = css.indexOf(selector);
  if (i < 0) throw new Error(`no ${selector} block in tokens.css`);
  const open = css.indexOf("{", i);
  let depth = 0;
  for (let j = open; j < css.length; j++) {
    if (css[j] === "{") depth++;
    else if (css[j] === "}") {
      depth--;
      if (depth === 0) return css.slice(open + 1, j);
    }
  }
  throw new Error(`unterminated ${selector}`);
}
function vars(body) {
  const out = {};
  for (const m of body.matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}
const dark = vars(block(":root"));
const light = { ...dark, ...vars(block('[data-theme="light"]')) };

const KINDS = Object.keys(dark).filter((k) => k.startsWith("kind-")).map((k) => k.slice(5));
if (KINDS.length < 11) throw new Error(`only ${KINDS.length} kind hues found; expected 11+`);
// The semantic colours that share these screens: --err for error states, --del for removed diff
// lines (and, in much of the Console, errors too). A non-null second entry would paint a literal
// instead of the token; none is needed now that --err is declared.
const SEM = [["err", null], ["del", null], ["warn", null], ["ok", null], ["accent", null]];

// The variables go into a <style> rule, never an inline style="" attribute. Measured the hard
// way: several token values are font stacks containing double quotes ("Source Code Pro", …),
// which terminate the attribute — so every variable declared AFTER the fonts in tokens.css was
// silently dropped, which is all eleven --kind-* hues. The page rendered eleven colourless chips
// and the semantic swatches (declared before the fonts) looked fine, so nothing looked broken.
const varRule = (sel, v) =>
  `${sel} {\n${Object.entries(v).map(([k, val]) => `  --${k}: ${val};`).join("\n")}\n}`;

const theme = (t) => `
<section class="theme ${t}" data-theme="${t === "light" ? "light" : "dark"}">
  <h2>${t}</h2>
  <div class="row">${KINDS.map((k) => `
    <span class="chip" style="color:var(--kind-${k});background:color-mix(in srgb, var(--kind-${k}) 14%, transparent)">${k}</span>`).join("")}
  </div>
  <div class="row">${KINDS.map((k) => `
    <span class="badge" style="background:var(--kind-${k});color:${t === "light" ? "#fff" : "#0c0c0c"}">${k.slice(0, 2)}</span>`).join("")}
  </div>
  <div class="row sem">${SEM.map(([n, lit]) => `
    <span class="chip" style="color:${lit || `var(--${n})`};background:color-mix(in srgb, ${lit || `var(--${n})`} 18%, transparent)">--${n}</span>`).join("")}
  </div>
  <div class="row surfaces">
    <span class="surf" style="background:var(--bg)">bg</span>
    <span class="surf" style="background:var(--panel)">panel</span>
    <span class="surf" style="background:var(--bar)">bar</span>
    <span class="surf" style="background:var(--active-bg)">active</span>
  </div>
</section>`;

const html = `<!doctype html><meta charset="utf-8"><title>kind chips</title>
<style>
  body { margin: 0; font: 13px/1.4 system-ui, sans-serif; }
  .theme { padding: 16px 20px 22px; }
  .theme.dark { background: #1e1e1e; color: #ddd; }
  .theme.light { background: #ffffff; color: #222; }
  h2 { margin: 0 0 10px; font-size: 12px; letter-spacing: .08em; text-transform: uppercase; opacity: .6; }
  .row { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; align-items: center; }
  .chip { padding: 3px 9px; border-radius: 4px; font-weight: 600; font-size: 12px; }
  .badge { width: 26px; height: 26px; border-radius: 6px; display: inline-flex; align-items: center;
           justify-content: center; font-weight: 700; font-size: 11px; }
  .sem .chip { font-style: italic; }
  .surf { width: 54px; height: 26px; border-radius: 4px; display: inline-flex; align-items: center;
          justify-content: center; font-size: 10px; opacity: .8; outline: 1px solid #8884; }
${varRule(".theme.dark", dark)}
${varRule(".theme.light", light)}
</style>
${theme("dark")}${theme("light")}`;

fs.mkdirSync(OUT, { recursive: true });
const page = path.join(OUT, "chips.html");
fs.writeFileSync(page, html);

const shot = path.join(OUT, "kind-chips.png");
const args = [
  "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
  "--force-device-scale-factor=2",
  "--window-size=1000,470",
  `--screenshot=${shot}`,
  `file://${page}`,
];
const p = spawn("/usr/bin/chromium", args, { stdio: ["ignore", "inherit", "pipe"] });
let err = "";
p.stderr.on("data", (d) => { err += d; });
p.on("exit", (code) => {
  if (code !== 0) {
    console.error(err);
    process.exit(code || 1);
  }
  console.log(`kinds rendered (${KINDS.length}): ${KINDS.join(" ")}`);
  console.log(`page  ${page}`);
  console.log(`shot  ${shot}`);
});
