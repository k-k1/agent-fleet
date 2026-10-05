// Demo recordings — a scripted scenario played through the real Console, as an animated WebP.
//
//   node console/scripts/shots/demo.mjs --scenario day|phone|review|plan|unattended
//                                       [--locale en|ja] [--out docs/img]
//                                       [--port 8766] [--cdp-port 9224] [--keep-frames]
//
// Drives the real Console bundle against `server.mjs --demo <scenario>` (a stateful fixture fleet,
// demo/<scenario>.mjs) in headless Chromium, records it with CDP's screencast, and encodes the
// frames with demo-encode.py into docs/img/demo-<scenario>-<locale>.webp. Same stance as the
// screenshots (capture.mjs): every name, issue and line of code on screen is fictional, so nothing
// from a real fleet can reach a published file.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const SCENARIO = arg("scenario", "day");
const LOCALE = arg("locale", "en");
const OUT = path.resolve(ROOT, arg("out", "docs/img"));
const PORT = Number(arg("port", 8766));
const CDP_PORT = Number(arg("cdp-port", 9224));
const FPS = Number(arg("fps", 12));
const KEEP = argv.includes("--keep-frames");
const BASE = `http://127.0.0.1:${PORT}/`;

if (!/^[a-z]+$/.test(SCENARIO) || !fs.existsSync(path.join(HERE, "demo", `${SCENARIO}.mjs`))) {
  console.error(`demo: no scenario "${SCENARIO}" in ${path.join(HERE, "demo")}`);
  process.exit(2);
}
const S = await import(`./demo/${SCENARIO}.mjs`);

// The Console gets meta.width x meta.height; the caption band (demo-overlay.js) is added under it.
// 1280x800 at 1x suits the README column, which shows it at roughly two thirds, where the Console's
// text is still legible — a larger viewport shrinks everything further for no gain.
const M = { dpr: 1, mobile: false, band: 56, captionPx: 19, ...S.meta };
const W = M.width;
const H = M.height + M.band;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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
const FRAMES = fs.mkdtempSync(path.join(os.tmpdir(), `af-demo-${SCENARIO}-${LOCALE}-`));

const stub = spawn(
  process.execPath,
  [path.join(HERE, "server.mjs"), "--port", String(PORT), "--locale", LOCALE, "--demo", SCENARIO],
  { stdio: ["ignore", "ignore", "inherit"] },
);
const chrome = spawn(
  "/usr/bin/chromium",
  [
    "--headless=new",
    "--no-sandbox",
    "--disable-gpu",
    "--hide-scrollbars",
    `--force-device-scale-factor=${M.dpr}`,
    "--font-render-hinting=none",
    `--remote-debugging-port=${CDP_PORT}`,
    "--remote-allow-origins=*",
    // A desktop scenario gets a desktop pointer: headless otherwise reports a coarse one, and the
    // Console then shows the controls meant for touch screens on every row (see capture.mjs). A
    // phone scenario keeps the coarse pointer, which is exactly what a phone reports.
    ...(M.mobile ? [] : ["--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"]),
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

async function hover(finder, ms = 650) {
  const p = await pointOf(finder);
  // A finger does not glide between targets: on a phone it lands where it taps.
  await ev(`__demo.move(${p.x}, ${p.y}, ${M.mobile ? 0 : ms})`);
  if (M.mobile) await sleep(Math.min(ms, 350));
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: p.x, y: p.y });
  return p;
}

async function click(finder, ms = 650) {
  const p = await hover(finder, ms);
  await sleep(120);
  await ev(`__demo.ripple()`);
  if (M.mobile) {
    // A tap, not a click: a phone scenario must go through the touch path the Console sees there.
    const pt = [{ x: p.x, y: p.y }];
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: pt });
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(350);
    await ev(`__demo.hideCursor()`);
    return;
  }
  await cdp.send("Input.dispatchMouseEvent", { type: "mousePressed", x: p.x, y: p.y, button: "left", clickCount: 1 });
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: p.x, y: p.y, button: "left", clickCount: 1 });
}

// Types into whatever has focus, two characters at a time, so it reads as typing.
async function type(text, cps = 30) {
  for (let i = 0; i < text.length; i += 2) {
    await cdp.send("Input.insertText", { text: text.slice(i, i + 2) });
    await sleep(2000 / cps);
  }
}

async function waitFor(expr, timeout = 10000) {
  const until = Date.now() + timeout;
  while (!(await ev(expr))) {
    if (Date.now() > until) throw new Error(`timed out waiting for: ${expr}`);
    await sleep(200);
  }
}

const frames = [];
let started = 0;
// Starts the screencast. The scenario calls it once its first frame is set, so a file never opens
// on a boot screen or a half-drawn caption.
async function record() {
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
  started = Date.now();
}

const ctx = {
  locale: LOCALE,
  W,
  H: M.height,
  sleep,
  ev,
  pointOf,
  hover,
  click,
  type,
  waitFor,
  record,
  // The last visible element matching `sel` whose text matches `pattern` (demo-overlay.js).
  find: (sel, pattern = "") => `__demo.find(${JSON.stringify(sel)}, ${JSON.stringify(pattern)})`,
  phase: (p) => fetchJSON(`${BASE}__demo/phase?phase=${p}`, { method: "POST" }),
  caption: (n, text) => ev(`__demo.caption(${n}, ${JSON.stringify(text)})`),
};

try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  await fetchJSON(`${BASE}api/whoami`);

  const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
  cdp = await CDP.connect(target.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: M.dpr, mobile: M.mobile });
  if (M.mobile) await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });

  // The browser state a returning user would have: locale, theme, the scenario's saved pane layout
  // (af.layout2.<user>.<tenant>, console/src/layout/migrate.ts) and rail sections
  // (console/src/ui/Section.tsx), plus whatever else the scenario remembers.
  const seed = S.seed(LOCALE);
  const store = [
    ...Object.entries(seed.sections || {}).map(([id, v]) => [`af-section-${id}`, String(v)]),
    ["af-display-settings", JSON.stringify({ locale: LOCALE, theme: "dark" })],
    ["af-tenant", "demo"],
    ...(seed.layout ? [["af.layout2.demo@example.com.demo", JSON.stringify(seed.layout)]] : []),
    ...Object.entries(seed.storage || {}),
  ];
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try { ${store.map(([k, v]) => `localStorage.setItem(${JSON.stringify(k)}, ${JSON.stringify(v)});`).join(" ")} } catch (e) {}
      ${seed.init || ""}`,
  });
  await cdp.send("Page.navigate", { url: BASE });
  await waitFor(
    `!!document.querySelector("#root > *") && !/読み込み中…|Loading…/.test(document.body.innerText)${seed.ready ? ` && (${seed.ready})` : ""}`,
    20000,
  );
  await sleep(1500); // first poll round: panes and lists settle
  await ev(`window.__demoConfig = ${JSON.stringify({ band: M.band, captionPx: M.captionPx })};`);
  await ev(fs.readFileSync(path.join(HERE, "demo-overlay.js"), "utf8"));
  await sleep(600); // the Console re-lays out into the shortened #root

  await S.script(ctx);
  if (!started) throw new Error(`scenario ${SCENARIO} never called record()`);

  await cdp.send("Page.stopScreencast");
  const end = (Date.now() - started) / 1000;
  const manifest = path.join(FRAMES, "frames.json");
  fs.writeFileSync(manifest, JSON.stringify({ frames, end: Math.max(end, frames.at(-1)?.t ?? 0) }));
  console.log(`[demo] ${SCENARIO}/${LOCALE}: ${frames.length} screencast frames over ${end.toFixed(1)}s${KEEP ? ` in ${FRAMES}` : ""}`);

  const out = path.join(OUT, `demo-${SCENARIO}-${LOCALE}.webp`);
  const enc = spawn("python3", [path.join(HERE, "demo-encode.py"), manifest, out, "--fps", String(FPS)], {
    stdio: "inherit",
  });
  const code = await new Promise((r) => enc.on("exit", r));
  if (code !== 0) throw new Error(`demo-encode.py exited ${code}`);
} finally {
  cleanup();
}
