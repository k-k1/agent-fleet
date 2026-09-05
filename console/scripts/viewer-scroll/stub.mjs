// Stub Control Plane for the file viewer's scroll-memory harness.
//
// Built like the neighbouring scripts/mirror-scroll/stub.mjs — it serves the real bundle
// (console/dist) and answers only as much of the CP API as booting needs — and differs only in
// having `api/fs/file`. What that returns is decided by the extension of the requested path:
// `.md` gives Markdown, whose preview height grows after MarkdownView writes innerHTML, and
// anything else gives code. Restoring the position has to survive that jump, which is what makes
// this the material of the check.
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
/** Whether to answer as an editable file. Required for the view/edit tabs, the path on which the
 *  reading surface becomes hidden. */
const EDITABLE = arg("editable", "0") === "1";

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  // Drop .mjs and pdf.js's worker is served as application/octet-stream, is refused as a module
  // worker, falls back to the fake worker and parses on the main thread.
  ".mjs": "text/javascript; charset=utf-8", ".bcmap": "application/octet-stream",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp",
  ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".ico": "image/x-icon",
};

/** The same expression the edit tab verifies with (src/features/editor/buffer.ts). */
const revisionOf = (content) => "sha256:" + createHash("sha256").update(content, "utf8").digest("hex");

// ---- The files that get served ------------------------------------------------------
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

/** A plain multi-page PDF (base-14 Helvetica only, no embedded fonts). Same policy of keeping no
 *  binary in the repository as scripts/pdf/check.mjs; unlike that one, only the page count
 *  matters here, since all this needs is enough height to scroll, so it is assembled by hand. */
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
  // For a PDF the real api/fs/file returns no content: just binary:true, leaving the bytes to be
  // fetched from download. Answering with content here would take the viewer down another branch.
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
    // For an editable file the edit tab does not appear at all unless revision (the sha256 of
    // the content) matches the content. CodeEditor verifies with the same expression, so pass
    // the real value here too.
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
  if (p === "/api/events") return void res.writeHead(404).end(); // fall back to the REST pollers
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
