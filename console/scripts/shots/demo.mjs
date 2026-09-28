// README demo recording — the "A day with Agent Fleet" scenario as an animated WebP.
//
//   node console/scripts/shots/demo.mjs [--locale en|ja] [--out docs/img]
//                                       [--port 8766] [--cdp-port 9224] [--keep-frames]
//
// Drives the real Console bundle against `server.mjs --demo` (a stateful fixture fleet) in
// headless Chromium, records it with CDP's screencast, and encodes the frames with
// demo-encode.py. Same stance as the screenshots (capture.mjs): every name, issue and line of
// code on screen is fictional, so nothing from a real fleet can reach the published file.
//
// The one scene that is not the Console — the permission request answered from the session's
// Slack thread — is drawn by demo-overlay.js with the texts and buttons the chat bridge really
// posts. A real Slack cannot be put in a reproducible recording.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { demoIssues } from "./demo-fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const LOCALE = arg("locale", "en");
const OUT = path.resolve(ROOT, arg("out", "docs/img"));
const PORT = Number(arg("port", 8766));
const CDP_PORT = Number(arg("cdp-port", 9224));
const FPS = Number(arg("fps", 12));
const QUALITY = Number(arg("quality", 75));
const KEEP = argv.includes("--keep-frames");
const BASE = `http://127.0.0.1:${PORT}/`;

// A 1280x800 Console at 1x, plus the caption band under it (demo-overlay.js BAND). The README
// column shows it at roughly two thirds, where the Console's text is still legible; a larger
// viewport shrinks everything further for no gain.
const W = 1280;
const H = 800 + 56;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- what the viewer reads ----------------------------------------------------------
// The captions are the README's four steps (README.md / README.ja.md "A day with Agent Fleet"),
// shortened to one line each. The phone texts are the chat bridge's own strings:
// workspace/agent/internal/bridge/format.go (headline), slack_interact.go (button labels) and
// workspace/agent/internal/sessionx/bridge_answer.go (the line a press leaves behind).
const [ISSUE_CLAUDE, ISSUE_CODEX] = demoIssues(LOCALE);
const TEXT = {
  en: {
    cap: [
      "Hand one issue to Claude Code and another to Codex, each in its own git worktree.",
      "Close the laptop and leave. Both keep working on the server.",
      "One asks for permission. It arrives in the session's Slack thread — answer from your phone.",
      "Back at a desk: what finished, what waits on you, and each worktree's changes.",
    ],
    away: "Laptop closed",
    phone: {
      thread: "Thread",
      channel: "#agents",
      bot: "Agent Fleet",
      headline: "A tool permission is awaiting your approval",
      session: `"#${ISSUE_CLAUDE.number} ${ISSUE_CLAUDE.title}" (Claude Code)`,
      link: "Open in Console",
      replies: "1 reply",
      allow: "Allow",
      deny: "Deny",
      done: "✓ Allowed",
      compose: "Reply…",
    },
    splitDown: "Split down",
  },
  ja: {
    cap: [
      "Claude Code と Codex に別々の Issue を頼む。それぞれ自分の git worktree で。",
      "ノートを閉じて出かける。どちらもサーバーの上で作業を続ける。",
      "片方が許可を求める。依頼はセッションの Slack スレッドに届き、スマートフォンから答える。",
      "机に戻ると、どれが終わりどれがあなたを待っているか、各 worktree の変更まで分かる。",
    ],
    away: "ノートは閉じたまま",
    phone: {
      thread: "スレッド",
      channel: "#agents",
      bot: "Agent Fleet",
      headline: "ツール実行の許可待ちです",
      session: `「#${ISSUE_CLAUDE.number} ${ISSUE_CLAUDE.title}」（Claude Code）`,
      link: "Console で開く",
      replies: "1 件の返信",
      allow: "許可",
      deny: "拒否",
      done: "✓ 許可しました",
      compose: "返信する…",
    },
    splitDown: "下に分割",
  },
}[LOCALE];

// The layout a returning user's browser would restore (console/src/layout/types.ts, version 3):
// the webshop commit graph where the first session will open, the sessions overview beside it.
const LAYOUT = {
  version: 3,
  mode: "split",
  cols: [
    { id: "c1", rowRatio: 0.5, cells: [{ id: "g1", selectedViewId: "p1", views: [{ id: "p1", session: null, content: { kind: "scm", scmRepo: "webshop" }, wrap: null }] }] },
    { id: "c2", rowRatio: 0.5, cells: [{ id: "g2", selectedViewId: "p2", views: [{ id: "p2", session: null, content: { kind: "sessions", showStopped: false }, wrap: null }] }] },
  ],
  // The left column gets more: it ends up holding the finished session's chat over its diff.
  colRatios: [0.58, 0.42],
  activeCellId: "g1",
};
// Rail sections (console/src/ui/Section.tsx): the issue tracker the scenario starts from and the
// repo tree the worktrees appear in; the rest folded so both fit.
const SECTIONS = { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 1, files: 0 };

// ---- minimal CDP client (as in capture.mjs, plus events) -----------------------------
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.handlers = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.method) {
        this.handlers.get(msg.method)?.(msg.params);
        return;
      }
      const p = this.pending.get(msg.id);
      if (p) {
        this.pending.delete(msg.id);
        msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
      }
    });
  }
  on(method, fn) {
    this.handlers.set(method, fn);
  }
  send(method, params = {}) {
    const id = ++this.id;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => {
      ws.addEventListener("open", res, { once: true });
      ws.addEventListener("error", rej, { once: true });
    });
    return new CDP(ws);
  }
}

async function fetchJSON(url, init) {
  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(url, init);
      if (r.ok) return await r.json();
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error(`timeout waiting for ${url}`);
}

// ---- run ---------------------------------------------------------------------------
fs.mkdirSync(OUT, { recursive: true });
const FRAMES = fs.mkdtempSync(path.join(os.tmpdir(), `af-demo-${LOCALE}-`));

const stub = spawn(process.execPath, [path.join(HERE, "server.mjs"), "--port", String(PORT), "--locale", LOCALE, "--demo"], {
  stdio: ["ignore", "ignore", "inherit"],
});
const chrome = spawn(
  "/usr/bin/chromium",
  [
    "--headless=new",
    "--no-sandbox",
    "--disable-gpu",
    "--hide-scrollbars",
    "--force-device-scale-factor=1",
    "--font-render-hinting=none",
    `--remote-debugging-port=${CDP_PORT}`,
    "--remote-allow-origins=*",
    // A desktop pointer: headless otherwise reports a coarse one, and the Console then shows the
    // controls meant for touch screens on every row (see capture.mjs).
    "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4",
    `--lang=${LOCALE === "ja" ? "ja-JP" : "en-US"}`,
    "about:blank",
  ],
  { stdio: ["ignore", "ignore", "ignore"] },
);
const cleanup = () => {
  try { chrome.kill(); } catch {}
  try { stub.kill(); } catch {}
  if (!KEEP) fs.rmSync(FRAMES, { recursive: true, force: true });
};
process.on("exit", cleanup);
process.on("SIGINT", () => process.exit(1));

let cdp;
const ev = async (expression) => {
  const r = await cdp.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(`page: ${r.exceptionDetails.exception?.description || r.exceptionDetails.text}`);
  return r.result?.value;
};

// The centre of the element `finder` (a page expression) evaluates to, waiting for it to render.
// A step whose control never appears fails the run: a recording that silently skipped a step
// would still encode, and look fine until someone watched it.
async function pointOf(finder, timeout = 8000) {
  const until = Date.now() + timeout;
  for (;;) {
    const p = await ev(`(() => {
      const e = ${finder};
      if (!e) return null;
      e.scrollIntoView({ block: "nearest" });
      const r = e.getBoundingClientRect();
      return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) };
    })()`);
    if (p) return p;
    if (Date.now() > until) throw new Error(`not found: ${finder}`);
    await sleep(150);
  }
}

const find = (sel, pattern = "") => `__demo.find(${JSON.stringify(sel)}, ${JSON.stringify(pattern)})`;

async function hover(finder, ms = 650) {
  const p = await pointOf(finder);
  await ev(`__demo.move(${p.x}, ${p.y}, ${ms})`);
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x, y: p.y });
  return p;
}

async function click(finder, ms = 650) {
  const p = await hover(finder, ms);
  await sleep(120);
  await ev(`__demo.ripple()`);
  await cdp.send("Input.dispatchMouseEvent", { type: "mousePressed", x: p.x, y: p.y, button: "left", clickCount: 1 });
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: p.x, y: p.y, button: "left", clickCount: 1 });
}

async function waitFor(expr, timeout = 10000) {
  const until = Date.now() + timeout;
  while (!(await ev(expr))) {
    if (Date.now() > until) throw new Error(`timed out waiting for: ${expr}`);
    await sleep(200);
  }
}

const phase = (p) => fetchJSON(`${BASE}__demo/phase?phase=${p}`, { method: "POST" });
const caption = (n, text) => ev(`__demo.caption(${n}, ${JSON.stringify(text)})`);

try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  await fetchJSON(`${BASE}api/whoami`);

  const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
  cdp = await CDP.connect(target.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: 1, mobile: false });
  const seed = [
    ...Object.entries(SECTIONS).map(([id, v]) => [`af-section-${id}`, String(v)]),
    ["af-display-settings", JSON.stringify({ locale: LOCALE, theme: "dark" })],
    ["af-tenant", "demo"],
    ["af.layout2.demo@example.com.demo", JSON.stringify(LAYOUT)],
    // The Claude session's "Changed files" panel open, as someone who uses it would have left it.
    [`af.mirror-files-open.sd3k7qa`, "1"],
  ];
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { ${seed.map(([k, v]) => `localStorage.setItem(${JSON.stringify(k)}, ${JSON.stringify(v)});`).join(" ")} } catch (e) {}`,
  });
  await cdp.send("Page.navigate", { url: BASE });
  await waitFor(`!!document.querySelector(".wi-row") && !/読み込み中…|Loading…/.test(document.body.innerText)`, 20000);
  await sleep(1500); // first poll round: the overview and the commit graph settle
  await ev(fs.readFileSync(path.join(HERE, "demo-overlay.js"), "utf8"));
  await caption(1, TEXT.cap[0]);
  await sleep(600); // the Console re-lays out into the shortened #root

  // Recording starts only now, so the file opens on a settled Console rather than a boot screen.
  const frames = [];
  let t0 = 0;
  cdp.on("Page.screencastFrame", (p) => {
    const t = p.metadata.timestamp;
    if (!t0) t0 = t;
    const file = path.join(FRAMES, `${String(frames.length).padStart(5, "0")}.png`);
    fs.writeFileSync(file, Buffer.from(p.data, "base64"));
    frames.push({ file, t: t - t0 });
    void cdp.send("Page.screencastFrameAck", { sessionId: p.sessionId }).catch(() => {});
  });
  await cdp.send("Page.startScreencast", { format: "png", everyNthFrame: 1 });
  const started = Date.now();

  // ---- 1. two issues, two agents, two worktrees ----
  await sleep(1800);
  await click(find(".wi-row", `#${ISSUE_CLAUDE.number}`));
  await sleep(1000);
  await click(find(".ui-modal .ui-btn-default"));
  await sleep(900);
  await hover(find(".ui-modal .launch-sec-head", "worktree"));
  await sleep(900);
  await click(find(".ui-modal .ui-btn-primary"));
  await waitFor(`!!${find(".ovw-card", `#${ISSUE_CLAUDE.number}`)}`);
  await sleep(1800);

  await click(find(".wi-row", `#${ISSUE_CODEX.number}`));
  await sleep(800);
  await click(find(".ui-modal .ui-btn-default"));
  await sleep(800);
  await click(find(".ui-modal .seg-btn.kind-codex"));
  // codex's model list is fetched on selection; start only once the picker has it.
  await waitFor(`!document.querySelector(".model-picker-loading")`);
  await sleep(700);
  await click(find(".ui-modal .ui-btn-primary"));
  await waitFor(`!!${find(".ovw-card", `#${ISSUE_CODEX.number}`)}`);
  await sleep(2200);

  // ---- 2. the laptop closes ----
  await ev(`__demo.hideCursor()`);
  await caption(2, TEXT.cap[1]);
  await ev(`__demo.veil(true, "09:10 → 11:40", ${JSON.stringify(TEXT.away)})`);
  await phase("away");
  await sleep(3000);

  // ---- 3. the permission request, answered on the phone ----
  await caption(3, TEXT.cap[2]);
  await ev(`__demo.veil(true)`);
  await ev(`__demo.phone(true, ${JSON.stringify({ ...TEXT.phone, time: "11:40" })})`);
  await sleep(2200);
  await ev(`__demo.setFinger(true); __demo.move(${W / 2 + 120}, ${H - 60}, 0)`);
  const allow = await ev(`__demo.phoneAllowRect()`);
  await ev(`__demo.move(${Math.round(allow.x)}, ${Math.round(allow.y)}, 800)`);
  await ev(`__demo.ripple()`);
  await ev(`__demo.phonePress()`);
  // While the phone is up, the fleet moves on to "back at a desk": the Claude session finished,
  // Codex has a question. Its chat is opened from the repo tree behind the veil, so the Console
  // comes back showing the finished session.
  await phase("back");
  await sleep(1800);
  await ev(`(${find(".sess-row .sess-btn", `#${ISSUE_CLAUDE.number}`)})?.click()`);
  await waitFor(`!/working/.test((${find(".ovw-card", `#${ISSUE_CLAUDE.number}`)})?.className || "working") && !!document.querySelector(".mfl-row")`);
  await ev(`__demo.hideCursor(); __demo.setFinger(false)`);
  await ev(`__demo.phone(false)`);
  await sleep(700);

  // ---- 4. back at a desk ----
  await ev(`__demo.veil(false)`);
  await caption(4, TEXT.cap[3]);
  await sleep(1600);
  await hover(find(".ovw-card", `#${ISSUE_CODEX.number}`), 800);
  await sleep(1300);
  await hover(find(".ovw-card", `#${ISSUE_CLAUDE.number}`), 600);
  await sleep(1000);
  await click(find("button", `^${TEXT.splitDown}$`), 800);
  await sleep(700);
  await click(`${find(".mfl-name", "^validate\\.ts$")}?.closest(".mfl-row")`, 800);
  await waitFor(`[...document.querySelectorAll(".scmview .scm-scroll")].some((e) => /cart_total_zero/.test(e.textContent))`);
  await sleep(3800);

  await cdp.send("Page.stopScreencast");
  const end = (Date.now() - started) / 1000;
  const manifest = path.join(FRAMES, "frames.json");
  fs.writeFileSync(manifest, JSON.stringify({ frames, end: Math.max(end, frames.at(-1)?.t ?? 0) }));
  console.log(`[demo] ${frames.length} screencast frames over ${end.toFixed(1)}s${KEEP ? ` in ${FRAMES}` : ""}`);

  const out = path.join(OUT, `demo-${LOCALE}.webp`);
  const enc = spawn("python3", [path.join(HERE, "demo-encode.py"), manifest, out, "--fps", String(FPS), "--quality", String(QUALITY)], {
    stdio: "inherit",
  });
  const code = await new Promise((r) => enc.on("exit", r));
  if (code !== 0) throw new Error(`demo-encode.py exited ${code}`);
} finally {
  cleanup();
}
