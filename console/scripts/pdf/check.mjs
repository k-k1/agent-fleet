// PDF ペイン（docs/82）の描画ハーネス。
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
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const REPO = path.resolve(fileURLToPath(new URL("../../..", import.meta.url)));
const CONSOLE = path.join(REPO, "console");
const PDFJS = path.join(CONSOLE, "node_modules/pdfjs-dist");
const CHROMIUM = process.env.CHROMIUM || "/usr/bin/chromium";
const shotArg = process.argv.indexOf("--screenshot");
const SHOT = shotArg > 0 ? process.argv[shotArg + 1] : "";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const fail = [];
const ok = [];
const check = (cond, label, detail = "") => (cond ? ok : fail).push(label + (detail ? ` — ${detail}` : ""));

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
  const version = JSON.parse(fs.readFileSync(path.join(PDFJS, "package.json"), "utf8")).version;
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
}

// ---- 配る --------------------------------------------------------------------
function serve() {
  const types = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".pdf": "application/pdf", ".bcmap": "application/octet-stream" };
  const server = http.createServer((req, res) => {
    const file = path.join(www, decodeURIComponent(req.url.split("?")[0]));
    if (!file.startsWith(www) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" });
    fs.createReadStream(file).pipe(res);
  });
  return new Promise((r) => server.listen(0, "127.0.0.1", () => r({ server, port: server.address().port })));
}

// ---- CDP（素の WebSocket。puppeteer は使わない）------------------------------
async function browser() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-pdfchrome-"));
  const proc = spawn(CHROMIUM, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
    "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
    `--user-data-dir=${dir}`, "--window-size=1000,760", "about:blank",
  ], { stdio: ["ignore", "ignore", "ignore"] });
  let port = 0;
  for (let i = 0; i < 120 && !port; i++) {
    await sleep(100);
    try {
      port = Number(fs.readFileSync(path.join(dir, "DevToolsActivePort"), "utf8").split("\n")[0]) || 0;
    } catch {}
  }
  if (!port) throw new Error("chromium did not open a debugging port");
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const ws = new WebSocket(targets.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) pending.get(m.id)(m), pending.delete(m.id);
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const i = ++id;
      pending.set(i, (m) => (m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m.result)));
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  const evaluate = async (expression) => {
    const r = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "evaluate failed");
    return r.result.value;
  };
  const close = () => {
    ws.close();
    proc.kill();
    try {
      fs.rmSync(dir, { recursive: true, force: true });
    } catch {}
  };
  await send("Page.enable");
  await send("Runtime.enable");
  return { send, evaluate, close };
}

/** 条件が満たされるまで待つ。満たされなければ最後の値を返す。 */
async function until(evaluate, expression, want, tries = 100) {
  let last;
  for (let i = 0; i < tries; i++) {
    last = await evaluate(expression);
    if (want(last)) return last;
    await sleep(100);
  }
  return last;
}

// ---- 本体 --------------------------------------------------------------------
await makeSamplePdf();
await bundle();
const { server, port } = await serve();
const b = await browser();
try {
  await b.send("Page.navigate", { url: `http://127.0.0.1:${port}/index.html` });

  // 1. 6 ページぶんの枠が出て、メタが親へ渡る。
  const pages = await until(b.evaluate, "document.querySelectorAll('.pdfview-page').length", (n) => n === 6);
  check(pages === 6, "6 ページが並ぶ", `pages=${pages}`);
  const meta = await b.evaluate("window.__meta && window.__meta.pages");
  check(meta === 6, "onMeta がページ数を返す", `meta=${meta}`);

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
    const shot = await b.send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(SHOT, Buffer.from(shot.data, "base64"));
  }

  // 7. 壊れた PDF は、白い面ではなく理由を出す。
  await b.send("Page.navigate", { url: `http://127.0.0.1:${port}/index.html?src=/broken.pdf` });
  const failed = await until(b.evaluate, "document.querySelector('.pdfview.is-failed')?.textContent || ''", (s) => !!s);
  check(!!failed, "壊れた PDF で理由が出る", JSON.stringify(failed));
} finally {
  b.close();
  server.close();
}

for (const line of ok) console.log("  OK   " + line);
for (const line of fail) console.log("  NG   " + line);
console.log(fail.length ? `\n${fail.length} 件が NG` : `\n${ok.length} 件すべて OK`);
process.exit(fail.length ? 1 : 0);
