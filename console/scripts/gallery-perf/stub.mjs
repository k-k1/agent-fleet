// Stub Control Plane for the gallery perf harness (ADR 0080 follow-up).
//
// Same idea as scripts/mirror-poll/stub.mjs next door: serves the REAL console bundle and
// answers just enough of the CP's API surface (from scripts/shots/fixtures.mjs) for the shell
// to boot, but `api/fs/tree` reads REAL directories under the caller's home — including a real
// generated-images folder with 70-250 pictures — instead of a synthetic fixture. The point is to
// drive the actual grid, actual card count and actual `<img>` mount timing the browser sees.
//
// The one thing this stub does NOT do is decode real pixels: `fs_thumb.go`'s cost is already
// measured (95ms cold / 44µs cached, thumbSem=4) and is not what is under test here. Instead the
// thumbnail route REPRODUCES THAT SHAPE — a 4-wide concurrency gate, ~95ms on a cold key, ~0 on a
// warm one — and answers with a small generated PNG (same swatchPNG idea as scripts/shots). What
// is under test is the BROWSER side: how many requests a mount/scroll fires, and in what order.
//
//   node console/scripts/gallery-perf/stub.mjs [--port 8797]
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";
import * as fx from "../shots/fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DIST = path.resolve(HERE, "../../dist");
const HOME = os.homedir();

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8797));
const LOCALE = arg("locale", "ja");

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".woff2": "font/woff2",
};

function swatchPNG(key, edge) {
  const W = edge || 64;
  const H = W;
  let h = 2166136261;
  for (let i = 0; i < key.length; i++) h = Math.imul(h ^ key.charCodeAt(i), 16777619);
  const base = [64 + (Math.abs(h) % 120), 64 + (Math.abs(h >> 8) % 120), 64 + (Math.abs(h >> 16) % 120)];
  const raw = Buffer.alloc(H * (1 + W * 3));
  for (let y = 0; y < H; y++) {
    const row = y * (1 + W * 3);
    raw[row] = 0;
    for (let x = 0; x < W; x++) {
      const o = row + 1 + x * 3;
      const t = (x + y) / (W + H);
      raw[o] = Math.round(base[0] * (0.5 + t * 0.6));
      raw[o + 1] = Math.round(base[1] * (0.5 + t * 0.6));
      raw[o + 2] = Math.round(base[2] * (0.5 + t * 0.6));
    }
  }
  const chunk = (type, body) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(body.length);
    const td = Buffer.concat([Buffer.from(type, "ascii"), body]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td) >>> 0);
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(W, 0);
  ihdr.writeUInt32BE(H, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", zlib.deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}
const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();
function crc32(buf) {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return c ^ -1;
}

// ---- thumbnail concurrency + latency shape (mirrors fs_thumb.go, measured separately) --------
const THUMB_SEM = 4; // thumbSem
const COLD_MS = 95; // measured cold decode
const WARM_MS = 0.05; // measured cache hit (44µs), rounded up for setTimeout granularity
const WARM_WORKERS = 2; // warmThumbWorkers

const thumbCache = new Set(); // keys already decoded (cold cost paid)
let thumbInFlight = 0;
const thumbQueue = [];
function runThumb(key, fn) {
  return new Promise((resolve) => {
    thumbQueue.push({ key, fn, resolve });
    pump();
  });
}
function pump() {
  while (thumbInFlight < THUMB_SEM && thumbQueue.length) {
    const { key, fn, resolve } = thumbQueue.shift();
    thumbInFlight++;
    const cold = !thumbCache.has(key);
    setTimeout(() => {
      thumbCache.add(key);
      fn();
      thumbInFlight--;
      resolve();
      pump();
    }, cold ? COLD_MS : WARM_MS);
  }
}

// warmThumbDir equivalent: background, WARM_WORKERS-wide, best-effort, marks entries warm.
function warmDir(fullDir, edge, names) {
  let i = 0;
  const worker = () => {
    if (i >= names.length) return;
    const key = path.join(fullDir, names[i++]) + ":" + edge;
    setTimeout(() => {
      thumbCache.add(key);
      worker();
    }, COLD_MS);
  };
  for (let w = 0; w < WARM_WORKERS; w++) worker();
}

const IMAGE_RE = /\.(png|jpe?g|webp|gif|bmp|svg)$/i;

function realTree(relDir, warmEdge) {
  const full = path.join(HOME, relDir);
  const ents = fs.readdirSync(full, { withFileTypes: true });
  const out = [];
  for (const e of ents) {
    const st = fs.statSync(path.join(full, e.name));
    if (e.isDirectory()) {
      out.push({ name: e.name, type: "dir", mtime: Math.floor(st.mtimeMs / 1000) });
    } else if (IMAGE_RE.test(e.name)) {
      out.push({ name: e.name, type: "file", size: st.size, mtime: Math.floor(st.mtimeMs / 1000) });
    }
  }
  out.sort((a, b) => (a.type !== b.type ? (a.type === "dir" ? -1 : 1) : a.name < b.name ? -1 : 1));
  if (warmEdge > 0) {
    const files = ents.filter((e) => !e.isDirectory() && IMAGE_RE.test(e.name)).map((e) => e.name);
    files.sort((a, b) => fs.statSync(path.join(full, b)).mtimeMs - fs.statSync(path.join(full, a)).mtimeMs);
    warmDir(full, warmEdge, files.slice(0, 300));
  }
  return { path: relDir, entries: out, root: HOME };
}

// ---- minimal boot surface (copied shape from scripts/shots/server.mjs) -----------------------
const exact = {
  "/api/version": () => ({ version: "0.3.0", commit: "demo" }),
  "/api/whoami": () => ({ ...fx.USER, scheduler_enabled: true, role: "member" }),
  "/api/tenants": () => ({ tenants: [{ slug: "demo", name: "Demo Team", role: "member" }], super_admin: false }),
  "/api/workspace": () => ({ state: "running", bootPhase: "" }),
  "/api/sessions": () => ({ sessions: [] }),
  "/api/repos": () => ({ repos: [] }),
  "/api/connections": () => ({}),
  "/api/notifications-shaped": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/notifications": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/chat/conversations": () => ({ conversations: [] }),
  "/api/assistants": () => ({ assistants: [] }),
  "/api/work-items": () => ({ items: [] }),
  "/api/memos": () => ({ memos: [] }),
  "/api/memo-categories": () => ({ categories: [] }),
  "/api/schedules": () => ({ schedules: [] }),
  "/api/env/ui-prefs": () => ({}),
  "/api/update/status": () => ({ current: "0.3.0", latest: "0.3.0" }),
  "/api/usage": () => ({ agents: [] }),
  "/api/browser/pages": () => ({ pages: [] }),
  "/api/tts/speakers": () => ({ speakers: [] }),
  "/api/internal-git/repos": () => ({ repos: [] }),
  "/api/pat": () => ({}),
  "/api/tts/dict": () => ({ entries: [] }),
  "/api/env/ws-settings": () => ({}),
  "/api/fs/changes": () => ({ changes: [] }),
};
const re = [[/^\/api\/sessions\/([^/]+)\/messages$/, () => ({ status: "idle" })]];

const seenUnknown = new Set();
function apiBody(pathname, query) {
  if (exact[pathname]) return exact[pathname](query);
  for (const [rx, fn] of re) {
    const m = rx.exec(pathname);
    if (m) return fn(m, query);
  }
  if (!seenUnknown.has(pathname)) {
    seenUnknown.add(pathname);
    console.log("[stub] unhandled:", pathname);
  }
  return {};
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  const p = url.pathname;

  if (p === "/api/events") return void res.writeHead(404).end();

  if (p === "/api/fs/tree") {
    const relDir = url.searchParams.get("path") || "";
    const warmEdge = Number(url.searchParams.get("warm") || 0);
    try {
      const body = JSON.stringify(realTree(relDir, warmEdge));
      res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
      res.end(body);
    } catch (e) {
      res.writeHead(404, { "content-type": "application/json; charset=utf-8" });
      res.end(JSON.stringify({ error: { code: "not_dir", message: String(e) } }));
    }
    return;
  }

  if (p === "/api/fs/download") {
    const relPath = url.searchParams.get("path") || "";
    const thumb = Number(url.searchParams.get("thumb") || 0);
    const key = path.join(HOME, relPath) + ":" + thumb;
    const respond = () => {
      res.writeHead(200, { "content-type": "image/png", "cache-control": "private, max-age=60" });
      res.end(swatchPNG(key, thumb || 512));
    };
    if (thumb > 0) runThumb(key, respond);
    else respond(); // originals (lightbox) are not gated: only thumbnails are under test
    return;
  }

  if (p.startsWith("/api/")) {
    const body = JSON.stringify(apiBody(p, url.searchParams));
    res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
    res.end(body);
    return;
  }

  let file = path.join(DIST, p === "/" ? "index.html" : decodeURIComponent(p));
  if (!file.startsWith(DIST)) return void res.writeHead(403).end();
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(DIST, "index.html");
  const buf = fs.readFileSync(file);
  res.writeHead(200, { "content-type": MIME[path.extname(file)] || "application/octet-stream", "cache-control": "no-store" });
  res.end(buf);
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`[gallery-perf stub] http://127.0.0.1:${PORT} (locale=${LOCALE}, dist=${DIST}, home=${HOME})`);
});
