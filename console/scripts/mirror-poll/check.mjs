// What does an open mirror COST while nothing is happening?
//
// The mirror polls GET /sessions/{name}/messages for as long as it is on screen. On a phone that
// is the session's standing bill: every request keeps the radio out of idle, and every response
// re-sends the whole-session aggregates (files/tasks/answers) whether or not they moved — measured
// against a real 13 MiB transcript: 5–13 KiB raw, 0.9–2.6 KiB gzipped, per poll. On the receiving
// side, applying an identical payload still handed React new arrays, so the whole conversation was
// regrouped and re-rendered about once a second for no difference at all.
//
// This drives the real Console bundle in headless Chromium (raw CDP — no Playwright, no CP, no
// agent; the stub from ../mirror-scroll serves the API) and measures both halves over a fixed
// window on a session that is standing still:
//
//   * requests — counted from Network.requestWillBeSent, i.e. what the phone actually sends
//   * main-thread work — Performance.getMetrics (ScriptDuration / RecalcStyleCount / LayoutCount)
//
//   npm --prefix console run build          # console/dist must exist (the real bundle)
//   node console/scripts/mirror-poll/check.mjs
//   node console/scripts/mirror-poll/check.mjs --window 60 --runs 2
//
// Exit status is the check: 0 = the cadence eased off and the window stayed under budget. Run it
// against a build from before pollCadence.ts for the positive control — that one polls flat.
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const STUB = path.join(HERE, "..", "mirror-scroll", "stub.mjs");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8793));
const CDP_PORT = Number(arg("cdp-port", 9253));
const RUNS = Number(arg("runs", 1));
const WINDOW = Number(arg("window", 45)) * 1000; // how long to watch a session that is at rest
const MODE = arg("mode", "idle"); // idle | typing
const KEYS = Number(arg("keys", 30)); // typing mode: how many characters to type
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Budget for the measured window. The ladder (pollCadence.ts) spends 15s at 3s, then 15s at 8s,
// then 15s at 15s — 5+2+1 = 8 polls in 45s against the flat cadence's 15. The allowance is the
// boot round plus one late tick; a build without the ladder cannot fit under it.
const budget = (ms) => Math.ceil(ms / 3000) * 0.75;

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.polls = []; // ms timestamps of transcript requests, for the interval report
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.method === "Network.requestWillBeSent") {
        if (/\/messages\?/.test(msg.params?.request?.url || "")) this.polls.push(Date.now());
        return;
      }
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
    });
  }
  send(method, params = {}) {
    const id = ++this.id;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  async ev(expression) {
    const r = await this.send("Runtime.evaluate", { expression, returnByValue: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  }
  /** The three counters that say how much of the main thread the idle mirror is using. */
  async metrics() {
    const { metrics } = await this.send("Performance.getMetrics");
    const of = (n) => metrics.find((m) => m.name === n)?.value ?? 0;
    return { script: of("ScriptDuration"), style: of("RecalcStyleCount"), layout: of("LayoutCount") };
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
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(url, init);
      if (r.ok) return await r.json();
    } catch {
      /* not up yet */
    }
    await sleep(200);
  }
  throw new Error("timeout waiting for " + url);
}

const SESSION_A = "チェックアウトの入力検証"; // the stub's idle session (sk4rq2f)
const OPEN_SESSION = `(() => {
  const b = [...document.querySelectorAll(".sess-row .sess-btn")]
    .find((e) => (e.textContent || "").includes(${JSON.stringify(SESSION_A)}));
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;
const pane = { id: "p0", session: null, content: { kind: "terminal", chat: true }, wrap: null };
const layout = { cols: [{ id: "c0", rowRatio: 0.5, panes: [pane] }], colRatios: [1], activeId: "p0" };

async function typeChar(cdp, ch) {
  const common = { key: ch, text: ch, unmodifiedText: ch, windowsVirtualKeyCode: ch.toUpperCase().charCodeAt(0) };
  await cdp.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
  await cdp.send("Input.dispatchKeyEvent", { type: "char", ...common });
  await cdp.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
}

// typing: the other half of the same defect. Writing in the composer is MirrorView state, so every
// keystroke re-renders it — and with the conversation regrouped and every block rebuilt from
// scratch, the cost of one character is the cost of the whole transcript. Memoizing the grouping
// and the capability object (and memo()ing TranscriptTurn) is what makes a keystroke cost a
// keystroke. The budget is per character, so it does not depend on how many are typed.
async function runTyping(cdp) {
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not find the session row in the left pane");
  await sleep(9000);
  if ((await cdp.ev(`(() => { const t = document.querySelector(".mirror-input"); if (!t) return "none"; t.focus(); return "ok"; })()`)) !== "ok")
    throw new Error("no composer input (session not live?)");
  const before = await cdp.metrics();
  const t0 = Date.now();
  for (let i = 0; i < KEYS; i++) {
    await typeChar(cdp, "abcdefghij"[i % 10]);
    await sleep(120);
  }
  await sleep(500);
  const after = await cdp.metrics();
  const script = after.script - before.script;
  const per = (script * 1000) / KEYS;
  return {
    ok: per <= 12,
    note:
      `${KEYS} keystrokes in ${((Date.now() - t0) / 1000).toFixed(1)}s  ` +
      `script ${script.toFixed(2)}s = ${per.toFixed(1)}ms/key (budget 12)  ` +
      `style ${after.style - before.style}  layout ${after.layout - before.layout}`,
  };
}

async function run(cdp) {
  if (MODE === "typing") return runTyping(cdp);
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not find the session row in the left pane");
  await sleep(9000); // opening round: the tail window, its markdown, images, the first polls
  const before = await cdp.metrics();
  const from = cdp.polls.length;
  const t0 = Date.now();
  await sleep(WINDOW);
  const after = await cdp.metrics();
  const polls = cdp.polls.slice(from);
  const gaps = polls.map((t, i) => Math.round((t - (i ? polls[i - 1] : t0)) / 100) / 10);
  const cap = budget(WINDOW);
  // The count is the claim; the widening gap is what proves it was the ladder and not a stall.
  const eased = gaps.length > 1 && Math.max(...gaps) >= 7;
  return {
    ok: polls.length <= cap && eased,
    note:
      `${polls.length} polls in ${WINDOW / 1000}s (budget ${cap})  gaps ${gaps.join("/")}s  ` +
      `script ${(after.script - before.script).toFixed(2)}s  ` +
      `style ${after.style - before.style}  layout ${after.layout - before.layout}`,
  };
}

const stub = spawn(
  process.execPath,
  [STUB, "--port", String(PORT), "--turns", "200", "--images", "0", "--imgdelay", "0",
   "--mermaid", "0", "--shared", "0", "--paging", "0", "--pagesize", "400",
   "--working", "0", "--split", "0", "--asks", "1", "--longans", "1"],
  { stdio: ["ignore", "ignore", "inherit"] },
);
const chrome = spawn(
  "/usr/bin/chromium",
  ["--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
   `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank"],
  { stdio: ["ignore", "ignore", "ignore"] },
);
const cleanup = () => {
  try { chrome.kill(); } catch { /* already gone */ }
  try { stub.kill(); } catch { /* already gone */ }
};
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(1); });

let failed = 0;
try {
  await fetchJSON(`${BASE}api/whoami`);
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  console.log(
    MODE === "typing"
      ? `[mirror-poll] typing ${KEYS} characters into an open session`
      : `[mirror-poll] watching an idle session for ${WINDOW / 1000}s`,
  );
  for (let i = 0; i < RUNS; i++) {
    const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
    const cdp = await CDP.connect(target.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    await cdp.send("Network.enable");
    await cdp.send("Performance.enable");
    // A phone: this is the device the bill is paid on, and the narrow layout is the one that
    // shows a single session pane (no second pane polling alongside).
    await cdp.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
    await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 });
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `try {
        localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark"}');
        localStorage.setItem("af-tenant", "demo");
        localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout))});
      } catch (e) {}`,
    });
    await cdp.send("Page.navigate", { url: BASE });
    await sleep(5000); // boot + first poll round
    const r = await run(cdp);
    if (!r.ok) failed++;
    console.log(`  [${i + 1}/${RUNS}] ${r.ok ? "OK " : "NG "} ${r.note}`);
    cdp.ws.close();
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
  }
} finally {
  cleanup();
}
console.log(failed ? `\n[mirror-poll] FAILED: ${failed} run(s) over budget` : "\n[mirror-poll] OK");
process.exit(failed ? 1 : 0);
