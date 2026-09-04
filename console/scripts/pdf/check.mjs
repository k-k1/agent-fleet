// Rendering harness for the PDF pane (docs/log/82).
//
// What it protects:
//   1. That a picture is really drawn. A canvas can have the right size and be entirely blank
//      without anything looking broken in the DOM, so read the pixels and require a minimum
//      share of non-white ones.
//   2. That zooming does not break the reading position. Changing the scale changes every page
//      height, so keeping scrollTop as it was jumps to a different page (the anchor computation
//      in pdfPages.ts).
//   3. That canvases of off-screen pages are released. Otherwise scrolling a long document to
//      the end leaves one bitmap per page piled up in the tab.
//   4. That a corrupt or empty PDF does not silently become a blank surface.
//
// Needs neither a real backend nor the CP: PdfView and React are bundled into one file with
// esbuild, served by a plain http server and driven over CDP. The sample PDFs are produced on
// the spot with chromium's --print-to-pdf, so no binary is committed to the repository.
//
//   npm --prefix console run pdf:check
//   node console/scripts/pdf/check.mjs --screenshot /tmp/pdf.png
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checker, resolveChromium, serveDir, sleep, startBrowser, until } from "../lib/headless.mjs";

const REPO = path.resolve(fileURLToPath(new URL("../../..", import.meta.url)));
const CONSOLE = path.join(REPO, "console");
const PDFJS = path.join(CONSOLE, "node_modules/pdfjs-dist");
const CHROMIUM = resolveChromium();
const PDFJS_VERSION = JSON.parse(fs.readFileSync(path.join(PDFJS, "package.json"), "utf8")).version;
const ASSET_PREFIX = `/assets/pdfjs/${PDFJS_VERSION}/`;
const shotArg = process.argv.indexOf("--screenshot");
const SHOT = shotArg > 0 ? process.argv[shotArg + 1] : "";

const { check, report } = checker();

// How a "drawn pixel" is counted. Checks 2 and 7 must use the same expression: fix one only and
// the other lies in exactly the same way.
//
// Alpha has to be part of it. Without it an undrawn canvas (transparent = rgba(0,0,0,0)) counts
// as all ink, because `d[i] < 200` also matches 0, so ink returns 100% in the gap between
// PdfView giving the canvas a width and pdf.js painting it. `until()` returns on the first value
// past the threshold, so the run moves on without waiting for either paint or network and reads
// `requests` before any asset is fetched — a false red (measured: about 1 run in 29, with 0
// requests and ink=100.00% where a healthy run reads 4.39%). Extending the `until` deadline does
// not fix it; returning early is the problem. So count only opaque (alpha >= 250) non-white
// pixels. Check 0 below exercises this expression on a synthetic canvas, so a broken expression
// turns the expression red instead of the whole family.
const INK_FN = `((el) => {
  if (!el || !el.width || !el.height) return -1;
  const d = el.getContext('2d').getImageData(0, 0, el.width, Math.min(el.height, 400)).data;
  let n = 0;
  for (let i = 0; i < d.length; i += 4) {
    if (d[i + 3] < 250) continue;                                  // undrawn or translucent: skip
    if (d[i] < 200 || d[i + 1] < 200 || d[i + 2] < 200) n++;       // not white: ink
  }
  return n / (d.length / 4);
})`;
const inkOf = (sel) => `${INK_FN}(document.querySelector(${JSON.stringify(sel)}))`;

// ---- Working directory ------------------------------------------------------
const www = fs.mkdtempSync(path.join(os.tmpdir(), "af-pdfcheck-"));
process.on("exit", () => fs.rmSync(www, { recursive: true, force: true }));

// ---- Sample PDF (6 pages, Japanese, with a table) ----------------------------
async function makeSamplePdf() {
  const src = path.join(www, "src.html");
  const pages = [];
  for (let i = 1; i <= 6; i++) {
    pages.push(
      `<section style="${i > 1 ? "page-break-before:always;" : ""}">` +
        `<h1>ページ ${i}：日本語の見出し</h1><p>本文テキスト ${i}。${"あ".repeat(40)}</p>` +
        `<table><tr><th>項目</th><th>数量</th></tr><tr><td>りんご</td><td>${i * 3}</td></tr></table></section>`,
    );
  }
  fs.writeFileSync(
    src,
    `<!doctype html><meta charset="utf-8"><style>body{font-family:sans-serif;margin:24px}` +
      `table{border-collapse:collapse}td,th{border:1px solid #333;padding:4px 10px}</style>${pages.join("")}`,
  );
  const out = path.join(www, "sample.pdf");
  await run(CHROMIUM, ["--headless", "--disable-gpu", "--no-sandbox", `--print-to-pdf=${out}`, "file://" + src]);
  // Corrupt PDF: a real signature with no body. pdf.js answers with InvalidPDFException.
  fs.writeFileSync(path.join(www, "broken.pdf"), Buffer.from("%PDF-1.7\n" + "0".repeat(512)));
  return out;
}

// ---- Sample 2: a PDF that *needs* the bundled assets (no embedded fonts) -----
//
// sample.pdf above is baked by chromium, which embeds every font, so pdf.js never fetches a cMap
// or a standard-14 font: deleting bundle()'s whole asset copy still left all 11 checks OK
// (measured; the server-side log showed 0 requests for `/assets/pdfjs/<version>/`). What the
// bundled assets protect is what pdfjs.ts:11 states — that a Japanese PDF *without* embedded
// fonts renders neither as mojibake nor as blank — so a sample of that shape is assembled here
// by hand (keeping no binary in the repository, same as sample.pdf):
//   - `/UniJIS-UCS2-H` (predefined CMap, CIDFontType0 not embedded) -> cmaps/*.bcmap
//   - `/Symbol` (standard 14, not embedded) -> standard_fonts/*.pfb
// `/Helvetica` alone does not pull standard_fonts (measured: only the 2 cmaps requests). pdf.js
// substitutes system fonts for the base-14 faces it can, so standard_fonts is requested only
// once a face with no substitute, `/Symbol`, is present (3 requests). Without it the "sample
// that fetches the assets" only exercises one of the two halves.
// The body is ASCII only (CJK is written as hex codes), so character count equals byte count and
// the offsets line up.
function makeAssetPdf() {
  const content =
    "BT /F1 24 Tf 24 150 Td (Standard 14 Helvetica, not embedded) Tj ET\n" +
    "BT /F2 24 Tf 24 100 Td <65E5672C8A9E306E898B51FA3057> Tj ET\n" + // hex for a Japanese heading
    "BT /F3 24 Tf 24 60 Td (abgdez) Tj ET\n";
  const objs = [
    "<</Type/Catalog/Pages 2 0 R>>",
    "<</Type/Pages/Kids[3 0 R]/Count 1>>",
    "<</Type/Page/Parent 2 0 R/MediaBox[0 0 460 200]/Resources<</Font<</F1 5 0 R/F2 6 0 R/F3 9 0 R>>>>/Contents 4 0 R>>",
    `<</Length ${content.length}>>\nstream\n${content}endstream`,
    "<</Type/Font/Subtype/Type1/BaseFont/Helvetica/Encoding/WinAnsiEncoding>>",
    "<</Type/Font/Subtype/Type0/BaseFont/KozMinPr6N-Regular/Encoding/UniJIS-UCS2-H/DescendantFonts[7 0 R]>>",
    "<</Type/Font/Subtype/CIDFontType0/BaseFont/KozMinPr6N-Regular/CIDSystemInfo<</Registry(Adobe)/Ordering(Japan1)/Supplement 6>>/FontDescriptor 8 0 R/DW 1000>>",
    "<</Type/FontDescriptor/FontName/KozMinPr6N-Regular/Flags 6/FontBBox[-437 -340 1147 1317]/ItalicAngle 0/Ascent 1317/Descent -349/CapHeight 742/StemV 80>>",
    "<</Type/Font/Subtype/Type1/BaseFont/Symbol>>",
  ];
  let pdf = "%PDF-1.7\n";
  const offsets = [];
  for (const [i, body] of objs.entries()) {
    offsets.push(pdf.length);
    pdf += `${i + 1} 0 obj\n${body}\nendobj\n`;
  }
  const startxref = pdf.length;
  pdf += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
  for (const o of offsets) pdf += `${String(o).padStart(10, "0")} 00000 n \n`;
  pdf += `trailer\n<</Size ${objs.length + 1}/Root 1 0 R>>\nstartxref\n${startxref}\n%%EOF\n`;
  fs.writeFileSync(path.join(www, "assets.pdf"), Buffer.from(pdf, "latin1"));
}

function run(cmd, args) {
  return new Promise((res, rej) => {
    const p = spawn(cmd, args, { stdio: ["ignore", "ignore", "ignore"] });
    p.on("exit", (c) => (c === 0 ? res() : rej(new Error(`${cmd} exited ${c}`))));
    p.on("error", rej);
  });
}

// ---- Bundle (using the production PdfView as it is) -------------------------
async function bundle() {
  const entry = path.join(www, "entry.jsx");
  fs.writeFileSync(
    entry,
    `import { createRoot } from "react-dom/client";
     import { PdfView } from ${JSON.stringify(path.join(CONSOLE, "src/features/viewer/PdfView.tsx"))};
     const params = new URLSearchParams(location.search);
     const src = params.get("src") || "/sample.pdf";
     window.__meta = null;
     createRoot(document.getElementById("root")).render(
       <PdfView src={src} onMeta={(m) => { window.__meta = m; }} />,
     );`,
  );
  // `?url` is a suffix esbuild does not know. A small plugin resolves it by copying the worker
  // into www and returning its URL, which is what vite's `?url` means.
  const urlSuffix = {
    name: "url-suffix",
    setup(build) {
      build.onResolve({ filter: /\?url$/ }, (args) => ({ path: args.path, namespace: "af-url" }));
      build.onLoad({ filter: /.*/, namespace: "af-url" }, (args) => {
        const file = args.path.replace(/\?url$/, "");
        const from = file.startsWith(".") ? path.resolve(path.dirname(args.importer || CONSOLE), file) : path.join(CONSOLE, "node_modules", file);
        const name = path.basename(from);
        fs.copyFileSync(from, path.join(www, name));
        return { contents: `export default ${JSON.stringify("/" + name)};`, loader: "js" };
      });
    },
  };
  const esbuild = await import(path.join(CONSOLE, "node_modules/esbuild/lib/main.js"));
  const version = PDFJS_VERSION;
  for (const dir of ["cmaps", "standard_fonts"]) {
    fs.cpSync(path.join(PDFJS, dir), path.join(www, "assets/pdfjs", version, dir), { recursive: true });
  }
  await esbuild.build({
    entryPoints: [entry],
    bundle: true,
    format: "esm",
    outfile: path.join(www, "app.js"),
    jsx: "automatic",
    absWorkingDir: CONSOLE,
    // The entry point lives in a temp directory, from which console/node_modules is unreachable.
    nodePaths: [path.join(CONSOLE, "node_modules")],
    define: { __AF_PDFJS_VERSION__: JSON.stringify(version), "process.env.NODE_ENV": '"production"' },
    plugins: [urlSuffix],
    logLevel: "error",
  });
  fs.writeFileSync(
    path.join(www, "index.html"),
    `<!doctype html><meta charset="utf-8"><title>pdfcheck</title>
     <link rel="stylesheet" href="/viewer.css">
     <style>html,body,#root{height:100%;margin:0}#root{display:flex;flex-direction:column}
       body{background:#1e1e1e;--border:#444;--bar:#252526;--fg:#ddd;--muted:#999;--mono:monospace}</style>
     <div id="root"></div><script type="module" src="/app.js"></script>`,
  );
  fs.copyFileSync(path.join(CONSOLE, "src/features/viewer/viewer.css"), path.join(www, "viewer.css"));
  fs.cpSync(path.join(CONSOLE, "src/features/viewer/parts"), path.join(www, "parts"), { recursive: true, filter: (src) => !src.endsWith(".tsx") && !src.endsWith(".ts") }); // viewer.css is an index of @import only (without parts the shot comes out unstyled)
}

// ---- Main --------------------------------------------------------------------
await makeSamplePdf();
makeAssetPdf();
await bundle();
const { server, port, requests } = await serveDir(www);
const b = await startBrowser();
try {
  await b.goto(`http://127.0.0.1:${port}/index.html`);

  // 0. Positive control for the ink expression itself: three synthetic canvases built and
  // measured inside the browser. If this is not green, the waits in checks 2 and 7 wait for
  // nothing.
  const ink0 = await b.evaluate(`(() => {
    const c = document.createElement('canvas'); c.width = 40; c.height = 40;
    const g = c.getContext('2d');
    const blank = ${INK_FN}(c);                                   // sized but undrawn = transparent
    g.fillStyle = '#fff'; g.fillRect(0, 0, 40, 40);
    const white = ${INK_FN}(c);                                   // painted white = still nothing
    g.fillStyle = '#000'; g.fillRect(0, 0, 20, 40);
    const half = ${INK_FN}(c);                                    // left half painted black
    return { blank, white, half }; })()`);
  check(
    ink0.blank === 0 && ink0.white === 0 && ink0.half > 0.4,
    "the ink expression does not count an undrawn canvas as drawn",
    `undrawn=${(ink0.blank * 100).toFixed(2)}% white=${(ink0.white * 100).toFixed(2)}% half-black=${(ink0.half * 100).toFixed(2)}%`,
  );

  // 1. Six page frames appear and the metadata reaches the parent.
  const pages = await until(b.evaluate, "document.querySelectorAll('.pdfview-page').length", (n) => n === 6);
  check(pages === 6, "six pages are laid out", `pages=${pages}`);
  const meta = await b.evaluate("window.__meta && window.__meta.pages");
  check(meta === 6, "onMeta reports the page count", `meta=${meta}`);

  // 1.5 The CSS really applies. viewer.css is an index of @import only, so dropping the copy of
  // parts makes the shot come out unstyled while every check still reports OK (measured: ink
  // moved 56x, 1.78% -> 100.00%, and all 10 checks stayed OK). Before taking the shot, read one
  // computed style that differs from the default, so this does not depend on looking at it.
  const styled = await b.evaluate("getComputedStyle(document.querySelector('.pdfview')).display");
  check(styled === "flex", "the parts of viewer.css apply", `.pdfview display=${styled}`);

  // 2. The first page really shows a picture (share of non-white pixels).
  // Do not raise the lower bound (0.001): the value moves with the moment it is measured, since
  // until() returns on the first value past the threshold and so reads a canvas mid-paint
  // (measured: 1.07%-3.35% on a bare run). It also has to hold in CI, where the runner has no
  // Japanese fonts, renders tofu and reads 1.65% (see the notes in headless.mjs). Ink only ever
  // says that something was drawn.
  const inked = await until(b.evaluate, inkOf(".pdfview-canvas"), (v) => v > 0.001);
  check(inked > 0.001, "text is drawn on page 1", `ink=${(inked * 100).toFixed(2)}%`);

  // 3. The page number, and how it follows scrolling.
  const first = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  check(/^1 \//.test(first), "starts on page 1", `bar=${JSON.stringify(first)}`);
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 2000");
  const moved = await until(b.evaluate, "document.querySelector('.pdfview-pageno').textContent", (s) => !/^1 \//.test(s));
  check(!/^1 \//.test(moved), "the page number advances on scroll", `bar=${JSON.stringify(moved)}`);

  // 4. Zooming preserves the reading position (the page number).
  const before = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  const width0 = await b.evaluate("document.querySelector('.pdfview-page').getBoundingClientRect().width");
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  const width1 = await until(
    b.evaluate,
    "document.querySelector('.pdfview-page').getBoundingClientRect().width",
    (w) => w > width0 + 1,
  );
  check(width1 > width0 + 1, "+ makes the page larger", `${Math.round(width0)} -> ${Math.round(width1)}`);
  const after = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  check(after === before, "zooming does not change the page being read", `${before} -> ${after}`);

  // 5. Zoomed wider than the pane, the left edge of the page is still reachable (centred
  // overflow).
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  await sleep(300);
  const reach = await b.evaluate(
    `(() => { const s = document.querySelector('.pdfview-scroll'), p = document.querySelector('.pdfview-page');
       s.scrollLeft = 0;
       return Math.round(p.getBoundingClientRect().left - s.getBoundingClientRect().left); })()`,
  );
  check(reach >= 0, "the left edge of the page is reachable when zoomed", `left=${reach}px`);

  // 6. Canvases of pages far from the viewport give their area back.
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 0");
  await sleep(400);
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 1e7");
  const freed = await until(
    b.evaluate,
    "[...document.querySelectorAll('.pdfview-canvas')].filter((c) => c.width === 0).length",
    (n) => n > 0,
  );
  check(freed > 0, "off-screen pages release their canvas", `freed=${freed}`);

  if (SHOT) {
    await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 0");
    await sleep(600);
    await b.screenshot(SHOT);
  }

  // 7. The bundled assets are actually requested, and actually served.
  // Two halves: the request count is > 0, and none of those requests 404s. "No 404s" alone
  // passes trivially — never requesting anything also produces zero 404s.
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/assets.pdf`);
  await until(b.evaluate, "document.querySelectorAll('.pdfview-page').length", (n) => n === 1);
  const inkedAsset = await until(b.evaluate, inkOf(".pdfview-canvas"), (v) => v > 0.001);
  const asked = requests.filter((r) => r.path.startsWith(ASSET_PREFIX));
  const missed = asked.filter((r) => r.status !== 200);
  const kinds = new Set(asked.map((r) => r.path.slice(ASSET_PREFIX.length).split("/")[0]));
  check(
    kinds.has("cmaps") && kinds.has("standard_fonts"),
    "a PDF without embedded fonts requests the bundled assets (cMap + standard 14 fonts)",
    `${asked.length} requests for ${ASSET_PREFIX}: ${[...kinds].join(" ") || "(none)"}`,
  );
  check(
    missed.length === 0,
    "the bundled assets are served (no 404)",
    missed.length ? `404: ${[...new Set(missed.map((r) => r.path))].slice(0, 3).join(" ")}` : `all ${asked.length} requests 200`,
  );
  check(inkedAsset > 0.001, "text is drawn for a PDF without embedded fonts too", `ink=${(inkedAsset * 100).toFixed(2)}%`);

  // 8. A corrupt PDF states a reason instead of showing a blank surface.
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/broken.pdf`);
  const failed = await until(b.evaluate, "document.querySelector('.pdfview.is-failed')?.textContent || ''", (s) => !!s);
  check(!!failed, "a corrupt PDF shows a reason", JSON.stringify(failed));
} finally {
  b.close();
  server.close();
}

process.exit(report());
