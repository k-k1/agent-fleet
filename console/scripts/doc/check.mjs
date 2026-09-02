// Office 文書の簡易プレビュー（docs/log/82 §82.4）の検査。
//
// 何を守るためのものか: **WASM が実ブラウザで本当に読み込まれ、実 OOXML を変換できるか**。
// これは jsdom では絶対に分からない（WebAssembly の初期化も fetch も配信の
// Content-Type も絡む）。逆に、Markdown をどう描くかは MarkdownView 側の担当なので、
// ここでは MarkdownView を差し替えて**変換結果の文字列**を見る。
//
// 標本はこの場で組み立てる最小の OOXML（zip を自前で書く）。リポジトリにバイナリを
// 置かないための選択で、**「実文書での再現度」はここでは測っていない**（それは
// docs/log/82 §82.2 の実測に置いた）。
//
//   npm --prefix console run doc:check
//   node console/scripts/doc/check.mjs --screenshot /tmp/doc.png
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checker, serveDir, startBrowser, until } from "../lib/headless.mjs";

const CONSOLE = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));
const { check, report } = checker();
const shotArg = process.argv.indexOf("--screenshot");
const SHOT = shotArg > 0 ? process.argv[shotArg + 1] : "";
const www = fs.mkdtempSync(path.join(os.tmpdir(), "af-doccheck-"));
process.on("exit", () => fs.rmSync(www, { recursive: true, force: true }));

// ---- 最小の zip ライター（無圧縮 = stored）---------------------------------
const CRC = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return (buf) => {
    let c = -1;
    for (const b of buf) c = t[(c ^ b) & 0xff] ^ (c >>> 8);
    return (c ^ -1) >>> 0;
  };
})();

function zip(entries) {
  const locals = [];
  const central = [];
  let offset = 0;
  for (const [name, text] of Object.entries(entries)) {
    const data = Buffer.from(text, "utf8");
    const nameBuf = Buffer.from(name, "utf8");
    const crc = CRC(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(nameBuf.length, 26);
    locals.push(local, nameBuf, data);

    const dir = Buffer.alloc(46);
    dir.writeUInt32LE(0x02014b50, 0);
    dir.writeUInt16LE(20, 4);
    dir.writeUInt16LE(20, 6);
    dir.writeUInt32LE(crc, 16);
    dir.writeUInt32LE(data.length, 20);
    dir.writeUInt32LE(data.length, 24);
    dir.writeUInt16LE(nameBuf.length, 28);
    dir.writeUInt32LE(offset, 42);
    central.push(dir, nameBuf);
    offset += local.length + nameBuf.length + data.length;
  }
  const dirBuf = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(entries).length, 8);
  end.writeUInt16LE(Object.keys(entries).length, 10);
  end.writeUInt32LE(dirBuf.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, dirBuf, end]);
}

// ---- 標本（最小の docx / xlsx / pptx）--------------------------------------
const RELS = (target, type) =>
  `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
  `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/${type}" Target="${target}"/></Relationships>`;

const W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
const docx = zip({
  "[Content_Types].xml":
    `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
  "_rels/.rels": RELS("word/document.xml", "officeDocument"),
  "word/document.xml":
    `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="${W}"><w:body>` +
    `<w:p><w:r><w:t>四半期レポートの本文</w:t></w:r></w:p>` +
    `<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>太字の段落</w:t></w:r></w:p>` +
    `<w:tbl><w:tr><w:tc><w:p><w:r><w:t>りんご</w:t></w:r></w:p></w:tc>` +
    `<w:tc><w:p><w:r><w:t>12</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
    `</w:body></w:document>`,
});

const S = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
const xlsx = zip({
  "[Content_Types].xml":
    `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
    `<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
    `<Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
  "_rels/.rels": RELS("xl/workbook.xml", "officeDocument"),
  "xl/_rels/workbook.xml.rels":
    `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
    `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
  "xl/workbook.xml":
    `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="${S}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
    `<sheets><sheet name="売上" sheetId="1" r:id="rId1"/><sheet name="メモ" sheetId="2" r:id="rId2"/></sheets></workbook>`,
  "xl/worksheets/sheet1.xml":
    `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="${S}"><sheetData>` +
    `<row r="1"><c r="A1" t="inlineStr"><is><t>商品</t></is></c><c r="B1" t="inlineStr"><is><t>数量</t></is></c></row>` +
    `<row r="2"><c r="A2" t="inlineStr"><is><t>みかん</t></is></c><c r="B2"><v>34</v></c></row>` +
    `</sheetData></worksheet>`,
  "xl/worksheets/sheet2.xml":
    `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="${S}"><sheetData>` +
    `<row r="1"><c r="A1" t="inlineStr"><is><t>2枚目のシート</t></is></c></row></sheetData></worksheet>`,
});

const P = "http://schemas.openxmlformats.org/presentationml/2006/main";
const A = "http://schemas.openxmlformats.org/drawingml/2006/main";
const pptx = zip({
  "[Content_Types].xml":
    `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
    `<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>`,
  "_rels/.rels": RELS("ppt/presentation.xml", "officeDocument"),
  "ppt/_rels/presentation.xml.rels": RELS("slides/slide1.xml", "slide"),
  "ppt/presentation.xml":
    `<?xml version="1.0" encoding="UTF-8"?><p:presentation xmlns:p="${P}" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
    `<p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst></p:presentation>`,
  "ppt/slides/slide1.xml":
    `<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:p="${P}" xmlns:a="${A}"><p:cSld><p:spTree>` +
    `<p:sp><p:txBody><a:bodyPr/><a:p><a:r><a:t>スライドの見出し</a:t></a:r></a:p>` +
    `<a:p><a:r><a:t>箇条書きの項目</a:t></a:r></a:p></p:txBody></p:sp>` +
    `</p:spTree></p:cSld></p:sld>`,
});

fs.writeFileSync(path.join(www, "sample.docx"), docx);
fs.writeFileSync(path.join(www, "sample.xlsx"), xlsx);
fs.writeFileSync(path.join(www, "sample.pptx"), pptx);
// 拡張子は Office でも中身は zip ですらないファイル。黙って白い面にならないこと。
fs.writeFileSync(path.join(www, "broken.docx"), Buffer.from("not an office document at all"));

// ---- 束ねる（製品コードの DocPreview をそのまま使う）------------------------
const esbuild = await import(path.join(CONSOLE, "node_modules/esbuild/lib/main.js"));
const entry = path.join(www, "entry.jsx");
fs.writeFileSync(
  entry,
  `import { createRoot } from "react-dom/client";
   import { DocPreview } from ${JSON.stringify(path.join(CONSOLE, "src/features/viewer/DocPreview.tsx"))};
   const q = new URLSearchParams(location.search);
   createRoot(document.getElementById("root")).render(
     <DocPreview src={q.get("src") || "/sample.docx"} format={q.get("fmt") || undefined} />,
   );`,
);
// Markdown の描画は MarkdownView の担当。ここでは変換結果そのものを見たいので差し替える。
const stubMarkdown = {
  name: "stub-markdown",
  setup(build) {
    build.onResolve({ filter: /MarkdownView\.tsx$/ }, () => ({ path: "stub-markdown", namespace: "stub" }));
    build.onLoad({ filter: /.*/, namespace: "stub" }, () => ({
      contents: `export function MarkdownView({ source }) { return <pre data-md>{source}</pre>; }`,
      loader: "jsx",
      // 仮想モジュールには解決の起点が無い。JSX が差し込む react/jsx-runtime を
      // 引けるよう、console/ を起点に指定する。
      resolveDir: CONSOLE,
    }));
  },
};
// `?url` は esbuild が知らない接尾辞（vite の意味に合わせる）。WASM を www へ置いて URL を返す。
const urlSuffix = {
  name: "url-suffix",
  setup(build) {
    build.onResolve({ filter: /\?url$/ }, (args) => ({ path: args.path, namespace: "af-url" }));
    build.onLoad({ filter: /.*/, namespace: "af-url" }, (args) => {
      const from = path.join(CONSOLE, "node_modules", args.path.replace(/\?url$/, ""));
      const name = path.basename(from);
      fs.copyFileSync(from, path.join(www, name));
      return { contents: `export default ${JSON.stringify("/" + name)};`, loader: "js" };
    });
  },
};
await esbuild.build({
  entryPoints: [entry],
  bundle: true,
  format: "esm",
  outfile: path.join(www, "app.js"),
  jsx: "automatic",
  absWorkingDir: CONSOLE,
  nodePaths: [path.join(CONSOLE, "node_modules")],
  define: { "process.env.NODE_ENV": '"production"' },
  plugins: [stubMarkdown, urlSuffix],
  logLevel: "error",
});
fs.writeFileSync(
  path.join(www, "index.html"),
  `<!doctype html><meta charset="utf-8"><title>doccheck</title>
   <link rel="stylesheet" href="/viewer.css">
   <style>html,body,#root{height:100%;margin:0}#root{display:flex;flex-direction:column}
     body{background:#1e1e1e;color:#ddd;--border:#444;--bar:#252526;--fg:#ddd;--muted:#999;--mono:monospace}</style>
   <div id="root"></div><script type="module" src="/app.js"></script>`,
);
fs.copyFileSync(path.join(CONSOLE, "src/features/viewer/viewer.css"), path.join(www, "viewer.css"));
fs.cpSync(path.join(CONSOLE, "src/features/viewer/parts"), path.join(www, "parts"), { recursive: true, filter: (src) => !src.endsWith(".tsx") && !src.endsWith(".ts") }); // viewer.css は @import だけの索引（parts が無いと無スタイルで撮れてしまう）

// ---- 見る --------------------------------------------------------------------
const { server, port } = await serveDir(www);
const b = await startBrowser();
const md = () => b.evaluate("document.querySelector('[data-md]')?.textContent || ''");
const status = () => b.evaluate("document.querySelector('.docpreview-status')?.textContent || ''");
try {
  for (const [file, fmt, wants] of [
    ["sample.docx", "docx", ["四半期レポートの本文", "太字の段落", "りんご"]],
    ["sample.xlsx", "xlsx", ["売上", "メモ", "みかん", "34", "2枚目のシート"]],
    ["sample.pptx", "pptx", ["スライドの見出し", "箇条書きの項目"]],
  ]) {
    await b.goto(`http://127.0.0.1:${port}/index.html?src=/${file}&fmt=${fmt}`);
    const text = await until(b.evaluate, "document.querySelector('[data-md]')?.textContent || ''", (s) => !!s, 150);
    const missing = wants.filter((w) => !text.includes(w));
    check(missing.length === 0, `${file} が Markdown に変換される`, missing.length ? `欠け: ${missing.join(", ")}` : `${text.length} 文字`);
  }

  // 表は表のまま出る（GFM の行）。値だけ拾えても列が崩れていては読めない。
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/sample.xlsx&fmt=xlsx`);
  const sheet = await until(b.evaluate, "document.querySelector('[data-md]')?.textContent || ''", (s) => !!s, 150);
  check(/\|\s*みかん\s*\|\s*34\s*\|/.test(sheet), "表が GFM の表として出る", JSON.stringify(sheet.split("\n").find((l) => l.includes("みかん")) || ""));

  // 「簡易プレビュー」の断りが、読み始める前に見える位置にある。
  const note = await b.evaluate("document.querySelector('.docpreview-note')?.textContent || ''");
  check(note.includes("簡易プレビュー"), "簡易プレビューだと明示している", JSON.stringify(note.trim().slice(0, 30)));

  // CSS が本当に当たっている。viewer.css は @import だけの索引なので、parts の
  // コピーを落とすと **無スタイルのまま全項目 OK で撮れてしまう**（判定は 1 文字も
  // 変わらず、画像だけが別物になる）。撮る前に、既定値（16px）と違う計算後スタイルを
  // 1 つ見て、目視に頼らず落とす。
  const styled = await b.evaluate("getComputedStyle(document.querySelector('.docpreview-note')).fontSize");
  check(styled === "11px", "viewer.css の parts が当たっている", `.docpreview-note font-size=${styled}`);

  if (SHOT) await b.screenshot(SHOT);

  // 壊れたファイルは、白い面ではなく理由とダウンロード導線を出す。
  await b.goto(`http://127.0.0.1:${port}/index.html?src=/broken.docx&fmt=docx`);
  // 待つ条件は **assert する条件に揃える**。「非空で待つ」と読み込み中の
  // 「文書を変換しています…」で抜けてしまい、そのあと現れる導線を先に assert する
  // レースになる（変換が少しでも遅いと必ず負ける）。終端の状態まで待ってから読む。
  const hint = await until(
    b.evaluate,
    "[...document.querySelectorAll('.docpreview-status')].map((e) => e.textContent).join(' ')",
    (s) => s.includes("ダウンロード"),
    150,
  );
  const why = await b.evaluate("document.querySelector('.docpreview-status')?.textContent || ''");
  check(!!why, "壊れたファイルで理由が出る", JSON.stringify(why));
  check(hint.includes("ダウンロード"), "原本を開く導線を出す");
  check(await md() === "", "壊れたファイルで本文の面は出さない");
} finally {
  b.close();
  server.close();
}
process.exit(report());
