// Pick the 11th kind colour (ADR 0095, kind="muse") the way the 10th was picked.
//
// The rule the --kind-lcpp comment in styles/tokens.css set: a new kind hue must clear every
// existing --kind-* hue AND the semantic colours that share the same screens (an error red on a
// kind badge reads as an error state next to real ones), in BOTH themes, by a margin at least as
// good as the precedents — and the result has to be rendered before it is believed.
//
// Run: node console/scripts/kindcolor/muse.mjs
// It prints the comparison table for the candidates and the winner's numbers, which are the
// values quoted in the tokens.css comment. Nothing is written; this is the reasoning, kept so a
// twelfth kind does not start from scratch.

const hex = (h) => {
  const s = h.replace('#', '');
  const f = s.length === 3 ? s.split('').map((c) => c + c).join('') : s;
  return [0, 2, 4].map((i) => parseInt(f.slice(i, i + 2), 16));
};

// sRGB -> linear -> XYZ (D65) -> Lab
const lin = (c) => (c / 255 <= 0.04045 ? c / 255 / 12.92 : Math.pow((c / 255 + 0.055) / 1.055, 2.4));
const lab = (h) => {
  const [r, g, b] = hex(h).map(lin);
  const X = (0.4124564 * r + 0.3575761 * g + 0.1804375 * b) / 0.95047;
  const Y = 0.2126729 * r + 0.7151522 * g + 0.072175 * b;
  const Z = (0.0193339 * r + 0.119192 * g + 0.9503041 * b) / 1.08883;
  const f = (t) => (t > 216 / 24389 ? Math.cbrt(t) : (841 / 108) * t + 4 / 29);
  const [fx, fy, fz] = [f(X), f(Y), f(Z)];
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)];
};

// CIEDE2000.
function dE(h1, h2) {
  const [L1, a1, b1] = lab(h1);
  const [L2, a2, b2] = lab(h2);
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

const relLum = (h) => {
  const [r, g, b] = hex(h).map(lin);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a, b) => {
  const [l1, l2] = [relLum(a), relLum(b)].sort((x, y) => y - x);
  return (l1 + 0.05) / (l2 + 0.05);
};
const hue = (h) => {
  const [, a, b] = lab(h);
  return ((Math.atan2(b, a) * 180) / Math.PI + 360) % 360;
};

// The comparison set: every kind hue plus the semantic colours that share these screens.
// `sem:err` is --err (which --del now aliases). When this sweep was first run --err was declared
// nowhere and #f85149 was a literal in both themes (the lcpp round found this); the values now
// follow tokens.css.
const DARK = {
  claude: '#e0a45e', codex: '#4ec97a', cursor: '#d96ba1', agy: '#4285f4', kiro: '#a371f7',
  copilot: '#7d8590', opencode: '#aab4be', lcpp: '#d9c952', shell: '#46c9d0', ssm: '#6d8bf5',
  'sem:err': '#e06c75', 'sem:ok': '#67c23a', 'sem:warn': '#e6a23c', 'sem:accent': '#66ccff',
};
const LIGHT = {
  claude: '#8f4f13', codex: '#126b3b', cursor: '#a02a63', agy: '#0f5ec4', kiro: '#6b3ac9',
  copilot: '#30363d', opencode: '#575e68', lcpp: '#5c5400', shell: '#0f676d', ssm: '#2c39a3',
  'sem:err': '#a5281c', 'sem:ok': '#146c2e', 'sem:warn': '#8a5b00', 'sem:accent': '#1d4ed8',
};
// Every surface a kind badge is drawn on, so the contrast floor is the worst of them.
const DARK_BG = { bg: '#1e1e1e', panel: '#181818', bar: '#111111', activeBg: '#20303a' };
const LIGHT_BG = { bg: '#ffffff', panel: '#f4f5f7', bar: '#eef0f3', activeBg: '#d7e6fb' };

// The floors the existing palette already meets, so "acceptable" means "no worse than shipped".
// agy is the worst existing contrast (3.81 dark / 4.86 light per the tokens.css comment), and
// rovo's round accepted min dE 7.5 dark / 6.3 light as the precedent floor.
const FLOOR = { dE: 7.5, dELight: 6.3, contrast: 3.81, contrastLight: 4.86 };

function score(cand, set, bgs) {
  let minDE = Infinity;
  let worst = '';
  for (const [name, c] of Object.entries(set)) {
    const d = dE(cand, c);
    if (d < minDE) {
      minDE = d;
      worst = name;
    }
  }
  let minC = Infinity;
  for (const b of Object.values(bgs)) minC = Math.min(minC, contrast(cand, b));
  return { minDE, worst, minC };
}

// Candidates. The hue gaps in the shipped palette, by Lab hue angle: err 3°, claude 30°,
// warn 33°, lcpp 55°, ok/codex 100-142°, shell 183°, accent 212°, agy 217°, ssm 225°,
// kiro 265°, cursor 330°. The widest opening is 265→330 (magenta / orchid), then 142→183.
//
// Meta's own brand blue is NOT a candidate and that is deliberate: agy (#4285f4) and ssm
// (#6d8bf5) already hold that region, and this palette has always chosen distinguishability
// over brand fidelity — copilot was moved OFF purple and kiro took it (docs/log/43 §4-1).
//
// The list below is the sweep's own finalists plus the two hand-picked values it beat, kept so
// the improvement is visible rather than asserted. The sweep's answer sits at the chroma FLOOR of
// the band and a high lightness: it wins by being a pale orchid rather than a saturated one,
// which is what puts distance between it and kiro's saturated purple.
const CANDIDATES = [
  ['orchid-sweep-a', '#dbabe6', '#5f376b'], // hue 320 C36, L76 / L30 — the sweep's best
  ['orchid-sweep-b', '#d2a3de', '#5f376b'], // same hue, one step darker on the dark side
  ['orchid-sweep-c', '#d8ace8', '#5d386c'], // hue 318
  // Light-side variants at the same hue, more saturated: the first render showed #5f376b reading
  // as a grey plum next to kiro's vivid violet and cursor's magenta — a peer of the two greys
  // rather than of the coloured kinds. dE is not what noticed that; the picture was.
  ['orchid-light-c46', '#dbabe6', '#6b2d7e'],
  ['orchid-light-c54', '#dbabe6', '#73228c'],
  ['orchid-light-L36', '#dbabe6', '#7a3391'],
  ['orchid-hand-1', '#c77ddb', '#8b3fa0'],  // hand-picked first pass (beaten)
  ['magenta-hand', '#d977d0', '#992a8f'],   // hand-picked, cursor's side of the gap (beaten)
];

// --- sweep mode -------------------------------------------------------------------------
//
// `--sweep` scans Lab hue x chroma x lightness for the best worst-case rather than trusting a
// hand-picked shortlist. The hand-picked list stays because it is what a reader checks by eye;
// the sweep is what makes "this is the best available" a measurement instead of a claim.
function labToHex(L, C, h) {
  const hr = (h * Math.PI) / 180;
  const a = C * Math.cos(hr);
  const b = C * Math.sin(hr);
  const fy = (L + 16) / 116;
  const fx = fy + a / 500;
  const fz = fy - b / 200;
  const fi = (t) => (t ** 3 > 216 / 24389 ? t ** 3 : (116 * t - 16) / (24389 / 27));
  const [X, Y, Z] = [fi(fx) * 0.95047, fi(fy), fi(fz) * 1.08883];
  const r = 3.2404542 * X - 1.5371385 * Y - 0.4985314 * Z;
  const g = -0.969266 * X + 1.8760108 * Y + 0.041556 * Z;
  const bl = 0.0556434 * X - 0.2040259 * Y + 1.0572252 * Z;
  const enc = (c) => {
    const v = c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055;
    return Math.round(Math.min(1, Math.max(0, v)) * 255);
  };
  const [R, G, B] = [enc(r), enc(g), enc(bl)];
  // Reject anything that needed clamping: a clamped colour is not the Lab value asked for, so
  // its measured dE would describe a colour nobody would ship.
  const out = '#' + [R, G, B].map((v) => v.toString(16).padStart(2, '0')).join('');
  for (const [want, got] of [[r, R], [g, G], [bl, B]]) {
    const v = want <= 0.0031308 ? 12.92 * want : 1.055 * Math.pow(Math.max(0, want), 1 / 2.4) - 0.055;
    if (v < -0.002 || v > 1.002) return null;
    if (Math.abs(v * 255 - got) > 0.6) return null;
  }
  return out;
}

// The chroma band the shipped kinds occupy, and the hue exclusion zone around the semantic
// colours. Both are constraints that dE alone gets wrong, and the first sweep run proved it:
// unconstrained, the top results were all dusty low-chroma pinks at chroma 20. They score well
// (a muted colour is far from every saturated one) and they would be the wrong answer — a kind
// badge has to read as a COLOUR like the other ten, next to two grey kinds that already occupy
// "muted". And the objection to red in the lcpp round was categorical, not metric: a badge in
// the error hue reads as an error state whatever its dE, so the hue is excluded outright rather
// than allowed in on a good number.
const CHROMA_BAND = { dark: [36, 80], light: [24, 85] };  // measured: --chroma
const SEM_HUE_EXCLUDE = 18; // degrees either side of a semantic colour's Lab hue

function labChroma(h) {
  const [, a, b] = lab(h);
  return Math.hypot(a, b);
}

if (process.argv.includes('--chroma')) {
  for (const [theme, set] of [['dark', DARK], ['light', LIGHT]]) {
    const rows = Object.entries(set).map(([n, c]) => [n, labChroma(c), hue(c)]);
    console.log(`# ${theme} chroma / hue`);
    for (const [n, C, H] of rows.sort((x, y) => x[1] - y[1])) {
      console.log(`  ${n.padEnd(12)} ${set[n]}  C ${C.toFixed(0).padStart(3)}  hue ${H.toFixed(0).padStart(3)}°`);
    }
  }
  process.exit(0);
}

if (process.argv.includes('--sweep')) {
  const semHues = Object.entries(DARK).filter(([n]) => n.startsWith('sem:')).map(([, c]) => hue(c))
    .concat(Object.entries(LIGHT).filter(([n]) => n.startsWith('sem:')).map(([, c]) => hue(c)));
  // Circular hue distance. Written inverted the first time (`180 - d < …`), which excluded
  // everything EXCEPT the semantic hues — so the sweep's top results were all in the error red's
  // own hue, which is the one colour the lcpp round ruled out categorically. The printed table
  // is what showed it; a silent filter would have been believed.
  const nearSemantic = (h) => semHues.some((sh) => {
    const d = Math.abs(((h - sh + 540) % 360) - 180);
    return d < SEM_HUE_EXCLUDE;
  });
  const best = [];
  for (let h = 0; h < 360; h += 2) {
    if (nearSemantic(h)) continue;
    for (let C = CHROMA_BAND.dark[0]; C <= CHROMA_BAND.dark[1]; C += 4) {
      for (let Ld = 55; Ld <= 82; Ld += 3) {
        const d = labToHex(Ld, C, h);
        if (!d) continue;
        const sd = score(d, DARK, DARK_BG);
        if (sd.minDE < FLOOR.dE || sd.minC < FLOOR.contrast) continue;
        for (let Ll = 30; Ll <= 52; Ll += 2) {
          for (const Cl of [C, C + 6, C + 12]) {
          if (Cl < CHROMA_BAND.light[0] || Cl > CHROMA_BAND.light[1]) continue;
          const l = labToHex(Ll, Cl, h);
          if (!l) continue;
          const sl = score(l, LIGHT, LIGHT_BG);
          if (sl.minDE < FLOOR.dELight || sl.minC < FLOOR.contrastLight) continue;
          best.push({ h, C, Ld, Ll, d, l, worst: Math.min(sd.minDE, sl.minDE), sd, sl });
          }
        }
      }
    }
  }
  best.sort((a, b) => b.worst - a.worst);
  console.log(`# sweep: ${best.length} (hue, chroma, lightness) points clear both floors`);
  console.log('# top 12 by worst-case dE across the two themes');
  for (const r of best.slice(0, 12)) {
    console.log(
      `  hue ${String(r.h).padStart(3)}° C ${String(r.C).padStart(2)} `
      + `dark ${r.d} (L${r.Ld}) light ${r.l} (L${r.Ll})  `
      + `worst dE ${r.worst.toFixed(1)}  dark dE ${r.sd.minDE.toFixed(1)} vs ${r.sd.worst.padEnd(10)} c ${r.sd.minC.toFixed(2)}  `
      + `light dE ${r.sl.minDE.toFixed(1)} vs ${r.sl.worst.padEnd(10)} c ${r.sl.minC.toFixed(2)}`,
    );
  }
  process.exit(0);
}

console.log('# hue angles of the shipped palette (Lab)');
for (const [n, c] of Object.entries(DARK)) console.log(`  ${n.padEnd(12)} ${c}  ${hue(c).toFixed(0)}°`);

console.log('\n# candidates  (floors: dE >= 7.5 dark / 6.3 light, contrast >= 3.81 / 4.86)');
const rows = [];
for (const [name, d, l] of CANDIDATES) {
  const sd = score(d, DARK, DARK_BG);
  const sl = score(l, LIGHT, LIGHT_BG);
  const pass = sd.minDE >= FLOOR.dE && sl.minDE >= FLOOR.dELight
    && sd.minC >= FLOOR.contrast && sl.minC >= FLOOR.contrastLight;
  rows.push({ name, d, l, sd, sl, pass });
  console.log(
    `  ${name.padEnd(10)} dark ${d} hue ${hue(d).toFixed(0).padStart(3)}° `
    + `dE ${sd.minDE.toFixed(1).padStart(5)} (vs ${sd.worst.padEnd(12)}) contrast ${sd.minC.toFixed(2)}`
    + ` | light ${l} dE ${sl.minDE.toFixed(1).padStart(5)} (vs ${sl.worst.padEnd(12)}) contrast ${sl.minC.toFixed(2)}`
    + `  ${pass ? 'PASS' : 'fail'}`,
  );
}

const best = rows.filter((r) => r.pass).sort((a, b) => Math.min(b.sd.minDE, b.sl.minDE) - Math.min(a.sd.minDE, a.sl.minDE))[0];
if (!best) {
  console.log('\nno candidate cleared the floors');
  process.exit(1);
}
console.log(`\n# winner: ${best.name}  dark ${best.d} / light ${best.l}`);
console.log(`  min dE     ${best.sd.minDE.toFixed(1)} dark (vs ${best.sd.worst}) / ${best.sl.minDE.toFixed(1)} light (vs ${best.sl.worst})`);
console.log(`  min contrast ${best.sd.minC.toFixed(2)} dark / ${best.sl.minC.toFixed(2)} light`);
console.log('\n# full distance table for the winner');
for (const [theme, cand, set] of [['dark', best.d, DARK], ['light', best.l, LIGHT]]) {
  const list = Object.entries(set).map(([n, c]) => [n, dE(cand, c)]).sort((a, b) => a[1] - b[1]);
  console.log(`  ${theme}: ` + list.map(([n, v]) => `${n} ${v.toFixed(1)}`).join('  '));
}
