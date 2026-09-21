// Pick the stacking order of the usage view's kind series (features/usage/colors.ts).
//
// The colours themselves are NOT chosen here and must not be: --kind-* in tokens.css is the
// one source for a kind's identity colour (the one-source rule of agent-display-naming), and
// repainting it for a chart would mean the same agent is two colours in one product. What a
// chart needs and an identifier palette never promised is that TOUCHING bands differ — a
// stacked area chart puts every adjacent pair against each other, and this palette has pairs
// that are close under normal vision (the two greys) and pairs that collapse under colour
// vision deficiency (agy's blue against kiro's purple).
//
// So the order is the only free variable, and it is searched rather than arranged: over every
// permutation of the kinds, score the worst adjacent pair under normal vision and the three
// dichromacies, in both themes, and keep the permutation whose worst case is best.
//
// Run: node console/scripts/kindcolor/usageorder.mjs
// Nothing is written. It prints the winner and the runners-up, and the numbers it prints are
// the ones quoted in the colors.ts comment.

import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const TOKENS = join(HERE, '..', '..', 'src', 'styles', 'tokens.css');

// The kinds that can appear on the usage axis: every kind whose sessions carry a transcript
// the ledger folds. shell and ssm have none, so they never produce a series.
const KINDS = ['claude', 'codex', 'cursor', 'agy', 'kiro', 'copilot', 'opencode', 'lcpp', 'muse'];

// --- reading the real tokens ------------------------------------------------
//
// Comments are stripped BEFORE matching, and the count is asserted afterwards. Both because
// of the defect the chips.mjs harness shipped twice (ADR 0095 P2-8): a `--name: value;`
// regexp starts matching inside a comment that happens to contain one, silently swallowing
// the declaration after it. A harness that lies looks exactly like a harness that works.
function readPalettes() {
  const css = readFileSync(TOKENS, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
  // The file declares the dark palette first and then overrides it under the light theme, so
  // the FIRST occurrence of a token is dark and the LAST is light.
  const dark = {};
  const light = {};
  for (const m of css.matchAll(/--kind-([a-z]+)\s*:\s*(#[0-9a-fA-F]{3,8})\s*;/g)) {
    const [, kind, value] = m;
    if (!(kind in dark)) dark[kind] = value;
    light[kind] = value;
  }
  for (const k of KINDS) {
    if (!dark[k] || !light[k]) throw new Error(`no --kind-${k} in tokens.css: the scan is broken`);
    if (dark[k] === light[k]) throw new Error(`--kind-${k} is the same in both themes: the scan is broken`);
  }
  return { dark, light };
}

// --- colour maths (the same CIEDE2000 as muse.mjs) --------------------------

const hex = (h) => {
  const s = h.replace('#', '');
  const f = s.length === 3 ? s.split('').map((c) => c + c).join('') : s;
  return [0, 2, 4].map((i) => parseInt(f.slice(i, i + 2), 16));
};
const lin = (c) => (c / 255 <= 0.04045 ? c / 255 / 12.92 : Math.pow((c / 255 + 0.055) / 1.055, 2.4));
const unlin = (c) => (c <= 0.0031308 ? c * 12.92 : 1.055 * Math.pow(c, 1 / 2.4) - 0.055);
const labOf = (rgb) => {
  const [r, g, b] = rgb;
  const X = (0.4124564 * r + 0.3575761 * g + 0.1804375 * b) / 0.95047;
  const Y = 0.2126729 * r + 0.7151522 * g + 0.072175 * b;
  const Z = (0.0193339 * r + 0.119192 * g + 0.9503041 * b) / 1.08883;
  const f = (t) => (t > 216 / 24389 ? Math.cbrt(t) : (841 / 108) * t + 4 / 29);
  const [fx, fy, fz] = [f(X), f(Y), f(Z)];
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)];
};

function dE(lab1, lab2) {
  const [L1, a1, b1] = lab1;
  const [L2, a2, b2] = lab2;
  const C1 = Math.hypot(a1, b1);
  const C2 = Math.hypot(a2, b2);
  const Cb = (C1 + C2) / 2;
  const G = 0.5 * (1 - Math.sqrt(Math.pow(Cb, 7) / (Math.pow(Cb, 7) + Math.pow(25, 7))));
  const ap1 = a1 * (1 + G);
  const ap2 = a2 * (1 + G);
  const Cp1 = Math.hypot(ap1, b1);
  const Cp2 = Math.hypot(ap2, b2);
  const deg = (r) => ((r * 180) / Math.PI + 360) % 360;
  const hp1 = Cp1 === 0 ? 0 : deg(Math.atan2(b1, ap1));
  const hp2 = Cp2 === 0 ? 0 : deg(Math.atan2(b2, ap2));
  const dL = L2 - L1;
  const dC = Cp2 - Cp1;
  let dh = 0;
  if (Cp1 * Cp2 !== 0) {
    dh = hp2 - hp1;
    if (dh > 180) dh -= 360;
    if (dh < -180) dh += 360;
  }
  const dH = 2 * Math.sqrt(Cp1 * Cp2) * Math.sin((dh * Math.PI) / 360);
  const Lb = (L1 + L2) / 2;
  const Cpb = (Cp1 + Cp2) / 2;
  let hpb = hp1 + hp2;
  if (Cp1 * Cp2 !== 0) {
    if (Math.abs(hp1 - hp2) > 180) hpb += hpb < 360 ? 360 : -360;
    hpb /= 2;
  }
  const T = 1 - 0.17 * Math.cos(((hpb - 30) * Math.PI) / 180)
    + 0.24 * Math.cos((2 * hpb * Math.PI) / 180)
    + 0.32 * Math.cos(((3 * hpb + 6) * Math.PI) / 180)
    - 0.2 * Math.cos(((4 * hpb - 63) * Math.PI) / 180);
  const Sl = 1 + (0.015 * (Lb - 50) ** 2) / Math.sqrt(20 + (Lb - 50) ** 2);
  const Sc = 1 + 0.045 * Cpb;
  const Sh = 1 + 0.015 * Cpb * T;
  const Rt = -2 * Math.sqrt(Math.pow(Cpb, 7) / (Math.pow(Cpb, 7) + Math.pow(25, 7)))
    * Math.sin((60 * Math.exp(-(((hpb - 275) / 25) ** 2)) * Math.PI) / 180);
  return Math.sqrt((dL / Sl) ** 2 + (dC / Sc) ** 2 + (dH / Sh) ** 2 + Rt * (dC / Sc) * (dH / Sh));
}

// --- colour vision deficiency ----------------------------------------------
//
// Machado, Oliveira & Fernandes (2009) severity-1.0 matrices, applied in LINEAR RGB. They are
// the same matrices the common accessibility tools use, and severity 1.0 (dichromacy) is the
// right arm for a gate: a palette that survives the full loss survives every partial one.
const CVD = {
  protan: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deutan: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.011820, 0.042940, 0.968881]],
  tritan: [[1.255528, -0.076749, -0.178779], [-0.078411, 0.930809, 0.147602], [0.004733, 0.691367, 0.303900]],
};

const clamp01 = (x) => Math.min(1, Math.max(0, x));
function simulate(hexColor, kind) {
  const rgb = hex(hexColor).map(lin);
  if (kind === 'normal') return rgb;
  const m = CVD[kind];
  return m.map((row) => clamp01(row[0] * rgb[0] + row[1] * rgb[1] + row[2] * rgb[2]));
}
const toHex = (rgb) =>
  '#' + rgb.map((c) => Math.round(clamp01(unlin(c)) * 255).toString(16).padStart(2, '0')).join('');

// --- the search -------------------------------------------------------------

const VISIONS = ['normal', 'protan', 'deutan', 'tritan'];

function pairTables(palette) {
  // labs[vision][kindIndex]
  const labs = {};
  for (const v of VISIONS) labs[v] = KINDS.map((k) => labOf(simulate(palette[k], v)));
  const table = {};
  for (const v of VISIONS) {
    table[v] = KINDS.map((_, i) => KINDS.map((_, j) => (i === j ? Infinity : dE(labs[v][i], labs[v][j]))));
  }
  return table;
}

function* permutations(arr) {
  if (arr.length <= 1) {
    yield arr;
    return;
  }
  for (let i = 0; i < arr.length; i++) {
    const rest = [...arr.slice(0, i), ...arr.slice(i + 1)];
    for (const p of permutations(rest)) yield [arr[i], ...p];
  }
}

function scoreOrder(order, tables) {
  // The gate is the WORST adjacent pair anywhere: one indistinguishable boundary is enough to
  // make a band unreadable, and averaging would let eight good pairs pay for it.
  let worst = Infinity;
  let worstCVD = Infinity;
  let sum = 0;
  let n = 0;
  for (const [theme, table] of Object.entries(tables)) {
    for (const v of VISIONS) {
      for (let i = 0; i + 1 < order.length; i++) {
        const d = table[v][order[i]][order[i + 1]];
        worst = Math.min(worst, d);
        if (v !== 'normal') worstCVD = Math.min(worstCVD, d);
        sum += d;
        n++;
      }
    }
    void theme;
  }
  return { worst, worstCVD, mean: sum / n };
}

function report(order, tables) {
  const names = order.map((i) => KINDS[i]);
  const rows = [];
  for (const [theme, table] of Object.entries(tables)) {
    for (const v of VISIONS) {
      let min = Infinity;
      let pair = '';
      for (let i = 0; i + 1 < order.length; i++) {
        const d = table[v][order[i]][order[i + 1]];
        if (d < min) {
          min = d;
          pair = `${names[i]}|${names[i + 1]}`;
        }
      }
      rows.push(`  ${theme.padEnd(5)} ${v.padEnd(6)} min ΔE ${min.toFixed(1)}  (${pair})`);
    }
  }
  return rows.join('\n');
}

const { dark, light } = readPalettes();
console.log('palette read from tokens.css:');
for (const k of KINDS) console.log(`  ${k.padEnd(9)} dark ${dark[k]}  light ${light[k]}`);
console.log('\nunder deuteranopia (dark), for eyeballing:');
for (const k of KINDS) console.log(`  ${k.padEnd(9)} ${toHex(simulate(dark[k], 'deutan'))}`);

const tables = { dark: pairTables(dark), light: pairTables(light) };
const idx = KINDS.map((_, i) => i);

const scored = [];
for (const order of permutations(idx)) {
  const s = scoreOrder(order, tables);
  scored.push({ order: [...order], ...s });
}
// Best worst-case first; the mean breaks ties, because several permutations reach the same
// worst pair and the one that is better everywhere else is the one to take.
scored.sort((a, b) => b.worst - a.worst || b.mean - a.mean);

console.log(`\n${scored.length} permutations scored. Top 5:`);
for (const s of scored.slice(0, 5)) {
  console.log(`\n[${s.order.map((i) => KINDS[i]).join(', ')}]`);
  console.log(`  worst adjacent ΔE ${s.worst.toFixed(1)} (CVD ${s.worstCVD.toFixed(1)}), mean ${s.mean.toFixed(1)}`);
  console.log(report(s.order, tables));
}

// What the shipped order scores today, so the change is stated as a delta rather than as a
// number with nothing to compare it to. The two new kinds are appended the lazy way — which
// is exactly the thing this script exists to avoid doing by hand.
const SHIPPED = ['cursor', 'agy', 'claude', 'copilot', 'codex', 'kiro', 'opencode'];
const naive = [...SHIPPED, 'lcpp', 'muse'].map((k) => KINDS.indexOf(k));
const ns = scoreOrder(naive, tables);
console.log(`\nfor comparison — the shipped seven with the two new kinds appended:`);
console.log(`  [${naive.map((i) => KINDS[i]).join(', ')}]`);
console.log(`  worst adjacent ΔE ${ns.worst.toFixed(1)} (CVD ${ns.worstCVD.toFixed(1)}), mean ${ns.mean.toFixed(1)}`);
console.log(report(naive, tables));

// --- the picture ------------------------------------------------------------
//
// The numbers rank the permutations; a stack of bands is what a reader actually meets, and
// this palette's own history is that a number can be passed by a pair that still reads as one
// block (the two greys). `--render <file>` writes the chosen order as touching bands, in both
// themes and under each simulated vision, to be looked at.
const renderAt = process.argv.indexOf('--render');
if (renderAt > 0 && process.argv[renderAt + 1]) {
  const best = scored[0].order;
  const band = (palette, vision, bg) => {
    const cells = best
      .map((i) => `<div style="flex:1;background:${toHex(simulate(palette[KINDS[i]], vision))}"></div>`)
      .join('');
    return `<div style="margin:0 0 10px"><div style="font:12px system-ui;color:${bg === '#1e1e1e' ? '#ddd' : '#222'};margin-bottom:3px">${vision}</div>
      <div style="display:flex;height:46px;width:640px">${cells}</div></div>`;
  };
  const panel = (palette, bg) =>
    `<div style="background:${bg};padding:16px">${VISIONS.map((v) => band(palette, v, bg)).join('')}</div>`;
  const labels = best.map((i) => KINDS[i]).join(' → ');
  const html = `<!doctype html><meta charset="utf-8"><body style="margin:0;font:12px system-ui">
    <div style="padding:8px 16px;background:#000;color:#eee">${labels}</div>
    ${panel(dark, '#1e1e1e')}${panel(light, '#ffffff')}</body>`;
  writeFileSync(process.argv[renderAt + 1], html);
  console.log(`\nwrote ${process.argv[renderAt + 1]}`);
}
