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
// Backward paging (docs/decisions/0009 P2). With it on, only the tail PAGE of the transcript is
// served, firstLine/hasMore advertise that there is more above, and `before=` answers the page
// before it — the shape a real long session has, and the one the mirror's "load earlier messages"
// runs against. Off by default so every existing scenario keeps its single whole-transcript reply.
const PAGING = arg("paging", "0") === "1";
const PAGE = Number(arg("pagesize", 400)); // jsonl lines per window; the server clamps the limit
// A session mid-turn, whose live reply carries a huge work trace. Its parts grow one per poll,
// alternating tool-last and text-last — the shape that made workSplit come and go, taking the
// whole 作業過程 disclosure with it. Poll 3 reports idle for one round (claude's Stop hook / a TUI
// heal), which is what folds a turn that is still running.
const WORKING = arg("working", "0") === "1";
const WORK_ROWS = Number(arg("workrows", 30)); // tool+text pairs in that live trace
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

// The live reply of a working session: a long work trace, then the real answer. Parts are appended
// one per poll from there (see livePartsAt), alternating tool and text.
const LIVE_WORK = (() => {
  const parts = [];
  for (let i = 0; i < WORK_ROWS; i++) {
    parts.push({ kind: "tool", tool: i % 2 ? "Read" : "Bash", info: `工程 ${i}`, output: `${i} 件の一致\n`.repeat(6) });
    parts.push({ kind: "text", text: `${i} 番目の工程を終えた。${"あ".repeat(80)}` });
  }
  // Several screens of final answer, so a reader parked at its first line (where the mirror puts
  // them when a reply completes) is neither at the tail nor above the work trace — the one place
  // from which the trace's height is felt.
  parts.push({ kind: "text", text: Array.from({ length: 8 }, (_, i) => answer(`最終 ${i}`)).join("\n\n") });
  return parts;
})();
// Poll n's parts. The trailing parts alternate kind, so `workSplit` alternates between finding a
// boundary and finding none — and with no boundary the whole trace used to render inline.
const livePartsAt = (n) => {
  const extra = [];
  for (let k = 0; k < Math.max(0, n - 2); k++) {
    extra.push(k % 2 === 0 ? { kind: "tool", tool: "Write", info: `追記 ${k}` } : { kind: "text", text: "続けます。" });
  }
  return [...LIVE_WORK, ...extra];
};
// Poll counter per session — the working scenario's whole point is that consecutive polls differ.
const polls = new Map();

function messages(session, q) {
  // A `since=0` fetch is a reader opening the session from scratch, so the count restarts there:
  // the stub outlives a scenario's runs, and without this only the first run ever saw the idle
  // round (the later ones then silently watched an ordinary finished turn).
  const fresh = q.get("before") === null && Number(q.get("since") || 0) === 0;
  const n = fresh ? 0 : (polls.get(session) ?? -1) + 1;
  polls.set(session, n);
  // Poll 2 reads idle for one round while the turn is still going: the momentary idle that folds a
  // running reply. `finalizing` does not bridge it, because a partial reply is already in the
  // transcript (awaitingReply is false).
  // "idle" spelled out, not "": the mirror only takes a status it was actually sent
  // (`if (d.status)`), so an empty string leaves the previous one standing.
  const status = WORKING ? (n === 2 ? "idle" : "working") : "";
  const body = {
    name: session, cursor: TURNS * 2, status, alive: true,
    mode: "Default", tasks: [], pendingQuestions: null,
    jsonlLines: TURNS * 2, jsonlMtime: 1753600000,
    // Only a WINDOWED reply carries the window's edge; an incremental poll leaves it alone.
    // Repeating firstLine:0/hasMore:false on every poll (which this used to do) wipes out what the
    // tail reply just advertised, one poll after it arrived — there is then nothing above to load.
    ...(PAGING ? {} : { firstLine: 0, hasMore: false }),
  };
  // Line indices must differ per session (a real jsonl's do), or switching sessions would
  // reuse the previous one's anchored reply idx and mask a bug.
  const off = session === "sk4rq2f" ? 0 : 1000;
  const all = (off ? TURNS_BODY.map((t) => ({ ...t, idx: t.idx + off })) : TURNS_BODY).map((t) =>
    WORKING && t.idx === off + TURNS * 2 - 1 ? { ...t, parts: livePartsAt(n) } : t,
  );
  const window = (upto) => {
    // The tail `PAGE` lines below `upto` (a jsonl line number), as whole turns.
    const from = Math.max(0, upto - PAGE);
    const out = all.filter((t) => t.idx - off >= from && t.idx - off < upto);
    return { messages: out, firstLine: off + from, hasMore: from > 0 };
  };
  const before = q.get("before");
  if (PAGING && before !== null) {
    // "Load earlier messages": the page ENDING at the oldest line held. No reset — it is prepended.
    return { ...body, ...window(Number(before) - off) };
  }
  if (Number(q.get("since") || 0) !== 0) {
    // Incremental poll. An idle stub has nothing to add; a working one resends its live turn,
    // whose parts have grown (the mirror merges by idx, so this replaces rather than appends).
    if (!WORKING) return { ...body, messages: [] };
    return { ...body, messages: [all[all.length - 1]] };
  }
  if (PAGING) return { ...body, ...window(TURNS * 2), reset: true };
  return { ...body, messages: all, reset: true };
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
  console.log(
    `[mirror-scroll stub] :${PORT} turns=${TURNS} images=${IMAGES} imgdelay=${IMG_DELAY} mermaid=${MERMAID}` +
      `${PAGING ? ` paging=${PAGE}` : ""}${WORKING ? ` working rows=${WORK_ROWS}` : ""}`,
  ),
);
