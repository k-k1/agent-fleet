// ファイルビュアーの位置記憶ハーネス用のスタブ Control Plane。
//
// 隣の scripts/mirror-scroll/stub.mjs と同じ作り —— **本物のバンドル**（console/dist）を
// 配り、CP の API はブートに要る分だけ返す —— で、違うのは `api/fs/file` を持つことだけ。
// 何を返すかは**要求されたパスの拡張子**で決まる: `.md` なら Markdown（プレビューの
// 高さは MarkdownView が innerHTML を書いてから伸びる）、それ以外はコード。位置の復元は
// その段差を越えないといけないので、ここが検査の素になる。
//
//   node console/scripts/viewer-scroll/stub.mjs [--port 8793] [--lines 2000] [--editable 1]
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import * as fx from "../shots/fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DIST = path.resolve(HERE, "../../dist");

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8793));
const LOCALE = arg("locale", "ja");
const LINES = Number(arg("lines", 2000));
/** 編集できるファイルとして返すか。表示⇄編集タブ（面が hidden になる経路）に要る。 */
const EDITABLE = arg("editable", "0") === "1";

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  // .mjs を落とすと pdf.js のワーカーが application/octet-stream で返り、モジュール
  // ワーカーとして拒否される → 「偽ワーカー」に落ちて解析がメインスレッドを占有する。
  ".mjs": "text/javascript; charset=utf-8", ".bcmap": "application/octet-stream",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp",
  ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".ico": "image/x-icon",
};

/** 編集タブの検算（src/features/editor/buffer.ts）と同じ式。 */
const revisionOf = (content) => "sha256:" + createHash("sha256").update(content, "utf8").digest("hex");

// ---- 配るファイル -------------------------------------------------------------------
const codeBody = (name) =>
  Array.from({ length: LINES }, (_, i) => `func step${i + 1}() { // ${name} の ${i + 1} 行目`).join("\n") + "\n";

const mdBody = (name) =>
  Array.from({ length: Math.ceil(LINES / 8) }, (_, i) =>
    [
      `## ${name} 第 ${i + 1} 節`,
      "",
      "本文の段落。プレビューの高さは innerHTML を書いたあとに決まるので、ここが遅れて伸びる。",
      "",
      "```go",
      `func step${i + 1}() int { return ${i + 1} }`,
      "```",
      "",
    ].join("\n"),
  ).join("\n");

/** 素の多ページ PDF（base-14 の Helvetica だけ・埋め込みフォント無し）。リポジトリに
 *  バイナリを置かない方針は scripts/pdf/check.mjs と同じで、あちらと違ってページ数だけが
 *  要る（スクロールできる高さがあればよい）ので手で組む。 */
function makePdf(pages = 30) {
  const objs = [];
  const pageIds = Array.from({ length: pages }, (_, i) => 4 + i * 2);
  objs.push("<</Type/Catalog/Pages 2 0 R>>");
  objs.push(`<</Type/Pages/Kids[${pageIds.map((id) => `${id} 0 R`).join(" ")}]/Count ${pages}>>`);
  objs.push("<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>");
  for (let i = 0; i < pages; i++) {
    const stream = `BT /F1 36 Tf 72 700 Td (Page ${i + 1}) Tj ET`;
    objs.push(
      `<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</F1 3 0 R>>>>/Contents ${pageIds[i] + 1} 0 R>>`,
    );
    objs.push(`<</Length ${stream.length}>>\nstream\n${stream}\nendstream`);
  }
  let pdf = "%PDF-1.7\n";
  const offsets = [];
  objs.forEach((body, i) => {
    offsets.push(pdf.length);
    pdf += `${i + 1} 0 obj\n${body}\nendobj\n`;
  });
  const startxref = pdf.length;
  pdf += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`;
  for (const o of offsets) pdf += `${String(o).padStart(10, "0")} 00000 n \n`;
  pdf += `trailer\n<</Size ${objs.length + 1}/Root 1 0 R>>\nstartxref\n${startxref}\n%%EOF\n`;
  return Buffer.from(pdf, "latin1");
}
const PDF = makePdf();

function fileBody(p) {
  const name = p.split("/").pop() || "file";
  // PDF は「中身を返さない」のが本物の api/fs/file の答え方（binary:true だけ返し、
  // 実バイトは download から取らせる）。ここを content 付きで返すとビュアーの分岐が変わる。
  if (name.endsWith(".pdf")) return { path: p, size: PDF.length, binary: true, truncated: false, editable: false };
  const content = name.endsWith(".md") ? mdBody(name) : codeBody(name);
  return {
    path: p,
    size: content.length,
    binary: false,
    truncated: false,
    editable: EDITABLE,
    editabilityReason: EDITABLE ? null : "read_only_root",
    content,
    // 編集できるファイルは revision（= 中身の sha256）が中身と一致していないと
    // 編集タブごと出ない。CodeEditor 側の検算と同じ式なので、ここでも本物を渡す。
    ...(EDITABLE ? { revision: revisionOf(content) } : {}),
  };
}

// ---- API ---------------------------------------------------------------------------
const exact = {
  "/api/version": () => ({ version: "0.3.0", commit: "demo" }),
  "/api/whoami": () => ({ ...fx.USER, scheduler_enabled: true, role: "member" }),
  "/api/tenants": () => ({ tenants: [{ slug: "demo", name: "Demo Team", role: "member" }], super_admin: false }),
  "/api/workspace": () => ({ state: "running", bootPhase: "" }),
  "/api/sessions": () => ({ sessions: fx.sessions(LOCALE) }),
  "/api/repos": () => ({ repos: fx.repos(LOCALE) }),
  "/api/connections": () => ({ claude: { connected: true } }),
  "/api/notifications-shaped": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/chat/conversations": () => ({ conversations: [] }),
  "/api/memos": () => ({ memos: [] }),
  "/api/schedules": () => ({ schedules: [] }),
  "/api/env/ui-prefs": () => ({}),
  "/api/update/status": () => ({ current: "0.3.0", latest: "0.3.0" }),
  "/api/browser/pages": () => ({ pages: [] }),
  "/api/tts/speakers": () => ({ speakers: [] }),
  "/api/shared-sessions": () => ({ sessions: [] }),
  "/api/session-shares": () => ({ shares: [] }),
  "/api/session-share-proposals": () => ({ proposals: [] }),
  "/api/fs/linemarks": () => ({}),
};

function apiBody(p, q) {
  if (p === "/api/fs/file") return fileBody(q.get("path") || "repos/shop/a.go");
  if (exact[p]) return exact[p](q);
  return {};
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  const p = url.pathname;
  if (p === "/api/events") return void res.writeHead(404).end(); // REST ポーラへ落とす
  if (p === "/api/fs/download") {
    res.writeHead(200, { "content-type": "application/pdf", "cache-control": "no-store" });
    return void res.end(PDF);
  }
  if (p.startsWith("/api/")) {
    res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
    return void res.end(JSON.stringify(apiBody(p, url.searchParams)));
  }
  let file = path.join(DIST, p === "/" ? "index.html" : decodeURIComponent(p));
  if (!file.startsWith(DIST)) return void res.writeHead(403).end();
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(DIST, "index.html");
  const buf = fs.readFileSync(file);
  res.writeHead(200, { "content-type": MIME[path.extname(file)] || "application/octet-stream", "cache-control": "no-store" });
  res.end(buf);
});

server.listen(PORT, "127.0.0.1", () =>
  console.log(`[viewer-scroll stub] :${PORT} lines=${LINES} editable=${EDITABLE ? 1 : 0}`),
);
