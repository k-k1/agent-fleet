// Stub Control Plane for the mirror scroll-landing harness.
//
// Same idea as the screenshot stub next door (scripts/shots/server.mjs) — it serves the
// REAL console bundle and answers just enough of the CP's API surface from that stub's
// fixtures — but the transcript endpoint is replaced by a synthetic, idle transcript whose
// SHAPE is the parameter: how many turns, and how much of the height arrives late.
//
// Late height is the whole point. MirrorView pins the bottom from a layout effect, but the
// turn bodies are filled by MarkdownView from a passive effect, so a transcript's height
// lands in several steps AFTER that pin. Big transcripts, mermaid diagrams and images that
// decode late are simply longer versions of the same thing, and are what strand the view
// above the end when the follow state is decided from raw scroll geometry.
//
//   node console/scripts/mirror-scroll/stub.mjs [--port 8791] [--turns 200]
//                                               [--images 3] [--imgdelay 3000] [--mermaid 0]
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";
import * as fx from "../shots/fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DIST = path.resolve(HERE, "../../dist");

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8791));
const LOCALE = arg("locale", "ja");
const TURNS = Number(arg("turns", 200));
const IMAGES = Number(arg("images", 3)); // trailing turns that carry a shared-file image
const IMG_DELAY = Number(arg("imgdelay", 3000)); // ms before those image bytes are served
const MERMAID = Number(arg("mermaid", 0)); // trailing turns that carry a mermaid diagram
// Outstanding handoff proposal: "" none | "mid" proposed a few turns back | "new" just
// proposed (nothing newer yet) | "launched" already used to start a session.
const HANDOFF = arg("handoff", "");
// --shared 1 seeds one received shared session (the shared section of the left pane). The default
// is zero, which hides the section entirely, so the mirror-side harness sees no difference.
const SHARED = arg("shared", "0") === "1";
const SHARED_ID = "cat-1";

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml", ".png": "image/png", ".webp": "image/webp",
  ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".ico": "image/x-icon",
};

// ---- the synthetic transcript ------------------------------------------------------
const CODE = [
  "```ts",
  "export function validateCart(cart: Cart): Result {",
  '  if (!cart.lines.length) return err("empty");',
  "  const total = cart.lines.reduce((s, l) => s + l.price * l.qty, 0);",
  '  if (total <= 0) return err("zero-total");',
  "  return ok(cart);",
  "}",
  "```",
].join("\n");

const answer = (i) =>
  `${i} 番目の応答。マークダウンは **innerHTML** で後から流し込まれる（＝高さが遅れて増える）。\n\n` +
  `- 検証経路を洗い直した\n- 合計金額のガードを足した\n- 回帰テストを 1 本足した\n\n` +
  CODE + `\n\n段落をもう一つ。${"あ".repeat(120)}\n`;

const diagram = (i) =>
  ["```mermaid", "flowchart TD", `  A${i}[開始] --> B${i}[カート検証]`, `  B${i} --> C${i}{合計 > 0?}`,
    `  C${i} -->|はい| D${i}[決済へ]`, `  C${i} -->|いいえ| E${i}[エラー表示]`, `  D${i} --> F${i}[完了]`,
    `  E${i} --> F${i}`, "```"].join("\n");

// Turn timestamps are RFC3339 strings, exactly as internal/transcript emits them — the
// mirror places the handoff card by comparing these against the proposal's created_at,
// so a stub that faked them as numbers would silently skip that comparison.
const T0 = Date.parse("2026-08-04T10:00:00.000Z");
const turnTS = (i) => new Date(T0 + i * 60_000).toISOString();

function buildTurns(n) {
  const t = [];
  for (let i = 0; i < n; i++) {
    const q = `質問 ${i}: 合計 0 円で決済に進めてしまう件を調べて`;
    t.push({ role: "user", idx: i * 2, ts: turnTS(i), text: q, parts: [{ kind: "text", text: q }] });
    const parts = [
      { kind: "text", text: `調べます（${i}）。` },
      { kind: "tool", tool: "Grep", info: "validateCart · src/", output: "src/checkout/validate.ts:4\nsrc/checkout/index.ts:22" },
      { kind: "tool", tool: "Read", info: "src/checkout/validate.ts", output: "42 行を読み込みました" },
      { kind: "text", text: answer(i) },
    ];
    if (IMAGES && i >= n - IMAGES) parts.push({ kind: "userfile", files: [`shot-${i}.png`], caption: "スクリーンショット" });
    if (MERMAID && i >= n - MERMAID) parts.push({ kind: "text", text: diagram(i) });
    t.push({ role: "assistant", idx: i * 2 + 1, ts: turnTS(i), model: "claude-opus-5", inTok: 1000, outTok: 100, text: "", parts });
  }
  return t;
}
const TURNS_BODY = buildTurns(TURNS);

function messages(session, q) {
  const body = {
    name: session, cursor: TURNS * 2, status: "", alive: true, // idle: no streaming follow
    firstLine: 0, hasMore: false, mode: "Default", tasks: [], pendingQuestions: null,
    jsonlLines: TURNS * 2, jsonlMtime: 1753600000,
  };
  if (Number(q.get("since") || 0) !== 0) return { ...body, messages: [] };
  // Line indices must differ per session (a real jsonl's do), or switching sessions would
  // reuse the previous one's anchored reply idx and mask a bug.
  const off = session === "sk4rq2f" ? 0 : 1000;
  const messagesOut = off ? TURNS_BODY.map((t) => ({ ...t, idx: t.idx + off })) : TURNS_BODY;
  return { ...body, messages: messagesOut, reset: true };
}

// ---- API surface -------------------------------------------------------------------
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
  // A shared session on the receiving side (docs/log/59). The same transcript and the same handoff
  // proposal as the owner's, returned through the shared API path, so that how the shared view
  // renders the body can be observed against the real bundle.
  "/api/shared-sessions": () => ({
    sessions: SHARED
      ? [{
          id: SHARED_ID, ownerUserKey: "owner-example-com", ownerEmail: "owner@example.com",
          name: "sk4rq2f", kind: "claude", repo: "shop", workingCopyId: "wc-1", branch: "develop",
          title: "チェックアウトの入力検証", state: "running", permission: "ro", workspaceState: "running",
        }]
      : [],
  }),
  "/api/session-shares": () => ({ shares: [] }),
  "/api/session-share-proposals": () => ({ proposals: [] }),
};
// The outstanding handoff proposal (docs: the card the mirror places by created_at).
// "mid" stamps it 3 turns before the end, so turns exist BELOW it — the shape that used
// to be impossible, because the card was always the scroller's last child.
//
// The shape is `{proposals: [...]}` - the post-fan-out shape, where one turn can branch into
// several successors. A stub that returns a different shape from the real thing measures the shape
// mismatch instead of the regression it is meant to detect: while this returned the singular
// `{proposal: …}`, the harness reported "no card" for all three cases.
function handoffProposal() {
  if (!HANDOFF) return { proposals: [] };
  const at = HANDOFF === "new" ? T0 + (TURNS + 5) * 60_000 : T0 + (TURNS - 3) * 60_000 + 1;
  return {
    proposals: [
      {
        id: "hp_stub",
        prompt: "次のセッションでやること:\n- 決済経路の回帰テストを追加\n- 合計 0 円のガードを検証",
        title: "決済バリデーションの続き",
        created_at: at,
        ...(HANDOFF === "launched" ? { launched_at: at + 60_000 } : {}),
      },
    ],
  };
}

const re = [
  [/^\/api\/sessions\/([^/]+)\/messages$/, (m, q) => messages(decodeURIComponent(m[1]), q)],
  [/^\/api\/sessions\/([^/]+)\/handoff-proposal$/, () => handoffProposal()],
  // The receiving side goes through CP. Both the transcript and the proposal return the same raw
  // material as the owner's side (CP's allowlist DTO only drops the coordinates; the body passes
  // through unchanged).
  [/^\/api\/shared-sessions\/([^/]+)\/messages$/, (_m, q) => messages("sk4rq2f", q)],
  [/^\/api\/shared-sessions\/([^/]+)\/handoff-proposals$/, () => handoffProposal()],
];

function apiBody(p, q) {
  if (exact[p]) return exact[p](q);
  for (const [rx, fn] of re) {
    const m = rx.exec(p);
    if (m) return fn(m, q);
  }
  return {};
}

// A real 900x600 PNG, so an image turn adds real height when it finally decodes.
let crcTable = null;
function crc32(buf) {
  if (!crcTable) {
    crcTable = [];
    for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crcTable[n] = c; }
  }
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
const PNG = (() => {
  const w = 900, h = 600, stride = w * 3 + 1;
  const raw = Buffer.alloc(stride * h);
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) { const o = y * stride + 1 + x * 3; raw[o] = 60; raw[o + 1] = 90; raw[o + 2] = 140; }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td));
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", zlib.deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
})();

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  const p = url.pathname;
  if (p === "/api/events") return void res.writeHead(404).end(); // fall back to the REST pollers
  if (p === "/api/fs/download") {
    // The late-layout knob: these bytes land after the transcript has already been pinned.
    setTimeout(() => {
      res.writeHead(200, { "content-type": "image/png", "cache-control": "no-store" });
      res.end(PNG);
    }, IMG_DELAY);
    return;
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
  console.log(`[mirror-scroll stub] :${PORT} turns=${TURNS} images=${IMAGES} imgdelay=${IMG_DELAY} mermaid=${MERMAID}`),
);
