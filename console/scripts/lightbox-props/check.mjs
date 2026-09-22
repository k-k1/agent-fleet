// The lightbox's properties panel, drawn from the REAL stylesheets in both themes, with the
// contrast of every line measured.
//
//   node console/scripts/lightbox-props/check.mjs [--out /tmp/imgprops]
//
// Why this exists. The panel used to take its surface from `var(--panel-bg, rgba(20,20,20,.92))`
// — and `--panel-bg` is declared nowhere, so EVERY theme got the dark literal while the text
// kept var(--fg). In light mode that is #1c2024 on near-black: the whole table was invisible,
// and nothing said so. A token that does not exist never announces itself; only the rendered
// colour does, which is why this is a render and not a grep.
//
// It loads tokens.css / ui.css / lightbox.css as they are on disk — a copy of the values here
// would keep passing after somebody edited the stylesheet, which is the one thing this is meant
// to catch — and composites the panel the way the browser stacks it: page background, the
// lightbox's 72% black backdrop, then the panel's own translucent fill.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checker, serveDir, startBrowser, until } from "../lib/headless.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const CONSOLE = path.resolve(HERE, "../..");
const OUT = (() => {
  const i = process.argv.indexOf("--out");
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : "/tmp/imgprops";
})();

/** WCAG AA for body text. The panel's rows are 11-12px, so the small-text threshold applies. */
const MIN_CONTRAST = 4.5;

const STYLES = {
  "tokens.css": "src/styles/tokens.css",
  "ui.css": "src/ui/ui.css",
  "lightbox.css": "src/features/viewer/parts/lightbox.css",
};

// The panel as ImageProps.tsx renders it, for a picture with a record and a session to jump to.
const PANEL = `
<div class="mirror-lightbox">
  <div class="imgprops">
    <div class="imgprops-madeby">
      <span class="imgprops-madeby-label">生成したセッション</span>
      <button type="button" class="imgprops-madeby-go">絵を描く</button>
    </div>
    <div class="imgprops-src">出どころ: PNG に埋まったグラフ</div>
    <dl class="imgprops-rows">
      <div class="imgprops-row"><dt>モデル</dt><dd id="VALUE">anime-aesthetic-v1</dd><button class="imgprops-copy">c</button></div>
      <div class="imgprops-row"><dt id="LABEL">seed</dt><dd>1275601790781520300</dd><button class="imgprops-copy">c</button></div>
    </dl>
    <div class="imgprops-actions">
      <button type="button" class="ui-btn ui-btn-ghost" id="ACTION">すべて JSON でコピー</button>
      <button type="button" class="ui-btn ui-btn-ghost">画像生成で開く</button>
    </div>
  </div>
</div>`;

const page = `<!doctype html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="/tokens.css"><link rel="stylesheet" href="/ui.css"><link rel="stylesheet" href="/lightbox.css">
<style>
  /* The overlay is position:fixed, so the two themes cannot simply sit side by side — each gets
     its own containing block. Only the frame is styled here; everything inside is the app's. */
  body { margin: 0; display: flex; font-family: sans-serif; }
  .frame { position: relative; flex: 1; height: 100vh; overflow: hidden; background: var(--bg); }
  .frame .mirror-lightbox { position: absolute; }
</style></head><body>
<div class="frame" data-theme="dark" id="dark">${PANEL}</div>
<div class="frame" data-theme="light" id="light">${PANEL}</div>
</body></html>`;

/**
 * A computed colour → [r, g, b, a] with the channels in 0-255.
 *
 * Two spellings, and the second is a trap: `color-mix()` computes to `color(srgb 0.956863
 * 0.960784 0.968627 / 0.94)` — channels in 0-1, not 0-255. Read as 0-255 the light panel became
 * rgb(0.96, 0.96, 0.97), i.e. black, and the check reported the light theme as unreadable while
 * its own screenshot showed it perfectly legible. A measurement that is wrong in the same
 * direction as the bug is the worst kind.
 */
const parse = (s) => {
  const n = s.match(/[\d.]+/g)?.map(Number) || [];
  const scale = s.startsWith("color(") ? 255 : 1;
  return [(n[0] ?? 0) * scale, (n[1] ?? 0) * scale, (n[2] ?? 0) * scale, n[3] ?? 1];
};

/** Composite a translucent colour over an opaque one — what the eye actually receives. */
const over = ([r, g, b, a], [br, bg, bb]) => [r * a + br * (1 - a), g * a + bg * (1 - a), b * a + bb * (1 - a), 1];

const lum = ([r, g, b]) =>
  [r, g, b]
    .map((v) => v / 255)
    .map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
    .reduce((acc, v, i) => acc + v * [0.2126, 0.7152, 0.0722][i], 0);

const contrast = (fg, bg) => {
  const [a, b] = [lum(fg), lum(bg)].sort((x, y) => y - x);
  return (a + 0.05) / (b + 0.05);
};

const main = async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-imgprops-"));
  for (const [name, src] of Object.entries(STYLES)) fs.copyFileSync(path.join(CONSOLE, src), path.join(dir, name));
  fs.writeFileSync(path.join(dir, "index.html"), page);

  const { server, port } = await serveDir(dir);
  const url = `http://127.0.0.1:${port}`;
  const browser = await startBrowser({ size: "1200,700" });
  const { check, report } = checker();
  try {
    await browser.goto(url + "/index.html");
    // `goto` returns at readyState "interactive", which is BEFORE a <link> stylesheet has been
    // applied — measured here: every colour read back as rgba(0, 0, 0, 0) while the screenshot
    // taken moments later was fully styled, i.e. the check failed on a page it had not waited
    // for. Wait for a token-derived colour to exist before reading any of them.
    const themed = await until(
      browser.evaluate,
      `getComputedStyle(document.getElementById("light")).backgroundColor`,
      (v) => v && v !== "rgba(0, 0, 0, 0)",
    );
    if (!themed || themed === "rgba(0, 0, 0, 0)") throw new Error("the stylesheets never applied");

    // Read every colour the panel stacks, per theme, straight out of the rendered page.
    const read = await browser.evaluate(`(() => {
      const out = {};
      for (const theme of ["dark", "light"]) {
        const frame = document.getElementById(theme);
        const q = (sel) => frame.querySelector(sel);
        const cs = (el, prop) => getComputedStyle(el).getPropertyValue(prop);
        out[theme] = {
          page: cs(frame, "background-color"),
          backdrop: cs(q(".mirror-lightbox"), "background-color"),
          panel: cs(q(".imgprops"), "background-color"),
          value: cs(q("#VALUE"), "color"),
          label: cs(q("#LABEL"), "color"),
          action: cs(q("#ACTION"), "color"),
          session: cs(q(".imgprops-madeby-go"), "color"),
        };
      }
      return out;
    })()`);

    if (process.env.DEBUG_PROPS) console.log(JSON.stringify(read, null, 2));
    for (const theme of ["dark", "light"]) {
      const r = read[theme];
      // Page → 72% black backdrop → the panel's own fill. Each step is what sits behind the next.
      const behind = over(parse(r.backdrop), parse(r.page));
      const surface = over(parse(r.panel), behind);
      // The panel must not be the SAME colour in both themes: that is exactly the bug — the dark
      // literal painted under light-theme text. Caught by the ratios below too, but named here so
      // a future regression reads as "the panel stopped following the theme".
      for (const [what, colour] of [
        ["value", r.value],
        ["label", r.label],
        ["action button", r.action],
        ["session jump", r.session],
      ]) {
        const ratio = contrast(over(parse(colour), surface), surface);
        check(ratio >= MIN_CONTRAST, `${theme}: ${what}`, `contrast ${ratio.toFixed(2)} (min ${MIN_CONTRAST})`);
      }
    }
    const samePanel = read.dark.panel === read.light.panel;
    check(!samePanel, "the panel follows the theme", samePanel ? `both themes paint ${read.dark.panel}` : "dark ≠ light");

    await browser.screenshot(path.join(OUT, "imgprops.png"));
    console.log(`shot: ${path.join(OUT, "imgprops.png")}`);
  } finally {
    browser.close();
    server.close();
    fs.rmSync(dir, { recursive: true, force: true });
  }
  process.exit(report());
};

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
