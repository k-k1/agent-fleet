// PDF ペイン（docs/log/82）の描画ハーネス。
//
// 何を守るためのものか:
//   1. **本当に絵が出ているか。** canvas は「大きさだけ正しくて中身が真っ白」でも
//      DOM 上は何も壊れて見えない。画素を読んで、白でない点が一定割合あることまで見る。
//   2. **拡大が読み位置を壊さないか。** 倍率を変えるとページの高さが全部変わるので、
//      scrollTop をそのまま残すと別のページへ飛ぶ（pdfPages.ts の anchor 計算）。
//   3. **画面外のページの canvas を捨てているか。** 捨てないと、長い文書を端まで送った
//      だけでページ数ぶんのビットマップがタブに積まれる。
//   4. **壊れた PDF / 空の PDF で、黙って白い面にならないか。**
//
// 実バックエンドも CP も要らない: PdfView を React ごと esbuild で 1 枚にまとめ、
// 素の http サーバで配って CDP で叩く。標本 PDF はその場で chromium の
// --print-to-pdf で作るので、リポジトリにバイナリを置かない。
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

// ---- 作業ディレクトリ -------------------------------------------------------
const www = fs.mkdtempSync(path.join(os.tmpdir(), "af-pdfcheck-"));
process.on("exit", () => fs.rmSync(www, { recursive: true, force: true }));

// ---- 標本 PDF（6 ページ・日本語・表つき）------------------------------------
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
  // 壊れた PDF: 署名だけ本物で中身が無い。pdf.js は InvalidPDFException を返す。
  fs.writeFileSync(path.join(www, "broken.pdf"), Buffer.from("%PDF-1.7\n" + "0".repeat(512)));
  return out;
}

// ---- 標本 2: **同梱アセットが要る** PDF（フォントを埋め込まない）--------------
//
// 🔴 上の sample.pdf は chromium が焼くので**フォントを全部埋め込む**。だから pdf.js は
// cMap も標準14フォントも 1 度も取りに来ず、**bundle() のアセットのコピーを丸ごと
// 消しても 11 件すべて OK のまま通っていた**（2026-09-04 実測。配信側の記録で
// `/assets/pdfjs/<version>/` への要求が 0 件だと分かった）。
// 同梱アセットが守っているのは pdfjs.ts:11 が書いているとおり
// 「**埋め込みの無い**日本語 PDF が化けない／空白にならない」ことなので、
// その形の標本をここで手で組む（リポジトリにバイナリを置かない方針は sample.pdf と同じ）:
//   - `/UniJIS-UCS2-H`（定義済み CMap・CIDFontType0 の埋め込み無し）→ cmaps/*.bcmap
//   - `/Symbol`（標準14・埋め込み無し）→ standard_fonts/*.pfb
// ⚠️ **`/Helvetica` では standard_fonts を取りに来ない**（2026-09-04 実測: 要求は cmaps の
// 2 件だけ）。pdf.js は base-14 のうち代替できるものをシステムフォントで賄うので、
// **代替の効かない `/Symbol` を入れて初めて standard_fonts が要求される**（3 件になる）。
// ——「アセットを取りに来る標本」を用意したつもりで、片方しか踏んでいない形。
// 中身は ASCII だけ（CJK は 16 進のコードで書く）ので、文字数＝バイト数で offset が合う。
function makeAssetPdf() {
  const content =
    "BT /F1 24 Tf 24 150 Td (Standard 14 Helvetica, not embedded) Tj ET\n" +
    "BT /F2 24 Tf 24 100 Td <65E5672C8A9E306E898B51FA3057> Tj ET\n" + // 日本語の見出し
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

// ---- 束ねる（製品コードの PdfView をそのまま使う）---------------------------
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
  // `?url` は esbuild が知らない接尾辞。ワーカーを www へ置き、その URL を返す小さな
  // プラグインで解決する（vite の `?url` と同じ意味になる）。
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
    // 入口は一時ディレクトリに置くので、そこから辿ると console/node_modules に届かない。
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
  fs.cpSync(path.join(CONSOLE, "src/features/viewer/parts"), path.join(www, "parts"), { recursive: true, filter: (src) => !src.endsWith(".tsx") && !src.endsWith(".ts") }); // viewer.css は @import だけの索引（parts が無いと無スタイルで撮れてしまう）
}

// ---- 本体 --------------------------------------------------------------------
await makeSamplePdf();
makeAssetPdf();
await bundle();
const { server, port, requests } = await serveDir(www);
const b = await startBrowser();
try {
  await b.goto(`http://127.0.0.1:${port}/index.html`);

  // 1. 6 ページぶんの枠が出て、メタが親へ渡る。
  const pages = await until(b.evaluate, "document.querySelectorAll('.pdfview-page').length", (n) => n === 6);
  check(pages === 6, "6 ページが並ぶ", `pages=${pages}`);
  const meta = await b.evaluate("window.__meta && window.__meta.pages");
  check(meta === 6, "onMeta がページ数を返す", `meta=${meta}`);

  // 1.5 CSS が本当に当たっている。viewer.css は @import だけの索引なので、parts の
  // コピーを落とすと **無スタイルのまま全項目 OK で撮れてしまう**（実測: ink が
  // 1.78% → 100.00% と 56 倍動いても 10 件すべて OK のまま）。撮る前に、
  // 既定値と違う計算後スタイルを 1 つ見て、目視に頼らず落とす。
  const styled = await b.evaluate("getComputedStyle(document.querySelector('.pdfview')).display");
  check(styled === "flex", "viewer.css の parts が当たっている", `.pdfview display=${styled}`);

  // 2. 1 枚目に本当に絵が出ている（白でない画素の割合）。
  const inked = await until(
    b.evaluate,
    `(() => { const c = document.querySelector('.pdfview-canvas');
       if (!c || !c.width) return -1;
       const d = c.getContext('2d').getImageData(0, 0, c.width, Math.min(c.height, 400)).data;
       let n = 0; for (let i = 0; i < d.length; i += 4) if (d[i] < 200 || d[i+1] < 200 || d[i+2] < 200) n++;
       return n / (d.length / 4); })()`,
    (v) => v > 0.001,
  );
  check(inked > 0.001, "1 ページ目に文字が描かれている", `ink=${(inked * 100).toFixed(2)}%`);

  // 3. ページ番号と、スクロールでの追従。
  const first = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  check(/^1 \//.test(first), "最初は 1 ページ目", `bar=${JSON.stringify(first)}`);
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 2000");
  const moved = await until(b.evaluate, "document.querySelector('.pdfview-pageno').textContent", (s) => !/^1 \//.test(s));
  check(!/^1 \//.test(moved), "スクロールでページ番号が進む", `bar=${JSON.stringify(moved)}`);

  // 4. 拡大しても読み位置（ページ番号）が保たれる。
  const before = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  const width0 = await b.evaluate("document.querySelector('.pdfview-page').getBoundingClientRect().width");
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  const width1 = await until(
    b.evaluate,
    "document.querySelector('.pdfview-page').getBoundingClientRect().width",
    (w) => w > width0 + 1,
  );
  check(width1 > width0 + 1, "＋ でページが大きくなる", `${Math.round(width0)} → ${Math.round(width1)}`);
  const after = await b.evaluate("document.querySelector('.pdfview-pageno').textContent");
  check(after === before, "拡大しても読んでいるページが変わらない", `${before} → ${after}`);

  // 5. ペインより広く拡大しても、ページの左端に手が届く（中央寄せの溢れ）。
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  await b.evaluate("[...document.querySelectorAll('.pdfview-bar button')].at(-1).click()");
  await sleep(300);
  const reach = await b.evaluate(
    `(() => { const s = document.querySelector('.pdfview-scroll'), p = document.querySelector('.pdfview-page');
       s.scrollLeft = 0;
       return Math.round(p.getBoundingClientRect().left - s.getBoundingClientRect().left); })()`,
  );
  check(reach >= 0, "拡大してもページの左端に届く", `left=${reach}px`);

  // 6. 画面から遠いページの canvas は面積を返している。
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 0");
  await sleep(400);
  await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 1e7");
  const freed = await until(
    b.evaluate,
    "[...document.querySelectorAll('.pdfview-canvas')].filter((c) => c.width === 0).length",
    (n) => n > 0,
  );
  check(freed > 0, "画面外のページは canvas を解放する", `freed=${freed}`);

  if (SHOT) {
    await b.evaluate("document.querySelector('.pdfview-scroll').scrollTop = 0");
    await sleep(600);
    await b.screenshot(SHOT);
  }

  // 7. **同梱アセットが実際に取りに来られて、配れている。**
  // 判定は「取りに来た件数 > 0」と「その要求が 404 でない」の 2 本立て。
  // ⚠️ **404 が 0 件だけでは通ってしまう**——1 度も取りに来なければ 404 も 0 件だから
  // （それが 2026-09-04 まで見えていなかった状態そのもの）。
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/assets.pdf`);
  await until(b.evaluate, "document.querySelectorAll('.pdfview-page').length", (n) => n === 1);
  const inkedAsset = await until(
    b.evaluate,
    `(() => { const c = document.querySelector('.pdfview-canvas');
       if (!c || !c.width) return -1;
       const d = c.getContext('2d').getImageData(0, 0, c.width, Math.min(c.height, 400)).data;
       let n = 0; for (let i = 0; i < d.length; i += 4) if (d[i] < 200 || d[i+1] < 200 || d[i+2] < 200) n++;
       return n / (d.length / 4); })()`,
    (v) => v > 0.001,
  );
  const asked = requests.filter((r) => r.path.startsWith(ASSET_PREFIX));
  const missed = asked.filter((r) => r.status !== 200);
  const kinds = new Set(asked.map((r) => r.path.slice(ASSET_PREFIX.length).split("/")[0]));
  check(
    kinds.has("cmaps") && kinds.has("standard_fonts"),
    "埋め込み無しの PDF が同梱アセットを取りに来る（cMap ＋ 標準14フォント）",
    `${ASSET_PREFIX} への要求 ${asked.length} 件: ${[...kinds].join(" ") || "（無し）"}`,
  );
  check(
    missed.length === 0,
    "同梱アセットを配れている（404 が無い）",
    missed.length ? `404: ${[...new Set(missed.map((r) => r.path))].slice(0, 3).join(" ")}` : `${asked.length} 件すべて 200`,
  );
  check(inkedAsset > 0.001, "埋め込み無しの PDF にも文字が描かれている", `ink=${(inkedAsset * 100).toFixed(2)}%`);

  // 8. 壊れた PDF は、白い面ではなく理由を出す。
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/broken.pdf`);
  const failed = await until(b.evaluate, "document.querySelector('.pdfview.is-failed')?.textContent || ''", (s) => !!s);
  check(!!failed, "壊れた PDF で理由が出る", JSON.stringify(failed));
} finally {
  b.close();
  server.close();
}

process.exit(report());
