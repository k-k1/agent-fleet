// Where does the mirror LAND when you open a session that already has history?
//
// Drives the real Console bundle (served by stub.mjs) with headless Chromium over raw CDP —
// no Playwright/Puppeteer, no CP, no workspace agent, no Docker — opens a session the way a
// user does (clicking its row in the left pane), and asserts the transcript ends up at its
// true bottom. jsdom cannot do this: the bug is entirely about layout timing.
//
//   npm --prefix console run build          # console/dist must exist (the real bundle)
//   node console/scripts/mirror-scroll/check.mjs
//   node console/scripts/mirror-scroll/check.mjs --runs 5 --scenario mermaid
//
// Exit status is the check: 0 = every run landed at the bottom.
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8791));
const CDP_PORT = Number(arg("cdp-port", 9251));
const RUNS = Number(arg("runs", 3));
const ONLY = arg("scenario", "");
const CPU = Number(arg("cpu", 4)); // main-thread throttling (see setCPUThrottlingRate below)
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Each scenario is a transcript shape whose height arrives late in a different pattern.
// "long" is the everyday one: a real session's tail window is hundreds of turns, and its
// markdown + highlighting + a screenshot land over several frames after the initial pin.
const SCENARIOS = [
  { name: "long", turns: 200, images: 3, imgdelay: 3000, mermaid: 0 },
  { name: "mermaid", turns: 12, images: 0, imgdelay: 0, mermaid: 3 },
  { name: "switch", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, from: "sc9lm3d" },
];

class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (p) { this.pending.delete(msg.id); msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result); }
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
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => { ws.addEventListener("open", res, { once: true }); ws.addEventListener("error", rej, { once: true }); });
    return new CDP(ws);
  }
}
async function fetchJSON(url, init) {
  for (let i = 0; i < 80; i++) {
    try { const r = await fetch(url, init); if (r.ok) return await r.json(); } catch { /* not up yet */ }
    await sleep(200);
  }
  throw new Error("timeout waiting for " + url);
}

// scrollTop/scrollHeight/clientHeight of the transcript, plus whether 最新へ is showing.
const PROBE = `(() => {
  const el = document.querySelector(".mirror-body");
  if (!el) return null;
  return {
    top: Math.round(el.scrollTop),
    gap: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
    turns: el.querySelectorAll("[data-turn-idx]").length,
    jump: !!document.querySelector(".mirror-jump"),
  };
})()`;
const WHEEL_UP = `(() => {
  const r = document.querySelector(".mirror-body").getBoundingClientRect();
  return JSON.stringify({ x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) });
})()`;
// The reader, parked at the bottom, expands the last 作業過程 disclosure. The content grows
// under them and they must STAY — snapping to the bottom would hide what they just opened
// (the regression ef94ece fixed, and the reason the re-pin used to be time-boxed).
const EXPAND_WORK = `(() => {
  const el = document.querySelector(".mirror-body");
  const heads = [...el.querySelectorAll(".mt-work-head")].filter((h) => h.getAttribute("aria-expanded") === "false");
  const h = heads[heads.length - 1];
  if (!h) return "none";
  h.scrollIntoView({ block: "center" });
  h.click();
  return "ok";
})()`;
const OPEN_SESSION = `(() => {
  const b = [...document.querySelectorAll(".sess-row .sess-btn")]
    .find((e) => (e.textContent || "").includes("チェックアウトの入力検証"));
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;

const pane = (session) => ({ id: "p0", session, content: { kind: "terminal", chat: true }, wrap: null });
const layout = (session) => ({ cols: [{ id: "c0", rowRatio: 0.5, panes: [pane(session)] }], colRatios: [1], activeId: "p0" });

async function runScenario(sc, chrome) {
  const stub = spawn(process.execPath, [path.join(HERE, "stub.mjs"), "--port", String(PORT),
    "--turns", String(sc.turns), "--images", String(sc.images), "--imgdelay", String(sc.imgdelay),
    "--mermaid", String(sc.mermaid)], { stdio: ["ignore", "ignore", "inherit"] });
  try {
    await fetchJSON(`${BASE}api/whoami`);
    const results = [];
    for (let run = 0; run < RUNS; run++) {
      const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
      const cdp = await CDP.connect(target.webSocketDebuggerUrl);
      await cdp.send("Page.enable");
      await cdp.send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
      // Throttle the main thread. This is not decoration: the bug lives in the window
      // between a programmatic scroll and the scroll EVENT it queues, and a busy or modest
      // machine is exactly what widens it. On an idle, unthrottled headless run the broken
      // build often lands correctly by luck, which is why the symptom read as intermittent.
      if (CPU > 1) await cdp.send("Emulation.setCPUThrottlingRate", { rate: CPU });
      // A returning user's browser: display settings + the saved pane layout. `from` seeds a
      // pane already showing ANOTHER session, so the scenario exercises a pane being reused.
      await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
        source: `try {
          localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark"}');
          localStorage.setItem("af-tenant", "demo");
          localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout(sc.from || null)))});
        } catch (e) {}`,
      });
      await cdp.send("Page.navigate", { url: BASE });
      await sleep(5000); // boot + first poll round
      const opened = await cdp.ev(OPEN_SESSION);
      if (opened !== "ok") throw new Error("could not find the session row in the left pane");
      await sleep(9000); // transcript render + late layout (images land at imgdelay)

      const landed = await cdp.ev(PROBE);

      // Expanding a disclosure is growth the READER caused — it must not be followed.
      const expanded = await cdp.ev(EXPAND_WORK);
      await sleep(1200);
      const afterExpand = await cdp.ev(PROBE);
      const keptPlace = expanded !== "ok" || afterExpand.gap > 2;

      // …and the other half of the contract: a real wheel-up must STOP the follow, show 最新へ,
      // and leave the reader where they are (no yank back to the bottom).
      const { x, y } = JSON.parse(await cdp.ev(WHEEL_UP));
      for (let i = 0; i < 4; i++) {
        await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: -400, pointerType: "mouse" });
        await sleep(80);
      }
      await sleep(1200);
      const up = await cdp.ev(PROBE);
      await sleep(2500);
      const still = await cdp.ev(PROBE);

      const stayedPut = Math.abs(still.top - up.top) < 5;
      const ok = landed && landed.gap <= 2 && !landed.jump && keptPlace && up.gap > 2 && up.jump && stayedPut;
      results.push({ ok, landedGap: landed?.gap, turns: landed?.turns, keptPlace, afterUpGap: up.gap, jumpShown: up.jump, stayedPut });
      console.log(`  [${sc.name} ${run + 1}/${RUNS}] ${ok ? "OK " : "NG "} landed gap=${landed?.gap}px (turns=${landed?.turns})  expand-kept-place=${keptPlace}  wheel-up: gap=${up.gap}px jump=${up.jump} stayedPut=${stayedPut}`);
      cdp.ws.close();
      await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
    }
    return results;
  } finally {
    stub.kill();
    await sleep(300);
  }
}

const chrome = spawn("/usr/bin/chromium", [
  "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
  `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank",
], { stdio: ["ignore", "ignore", "ignore"] });
const cleanup = () => { try { chrome.kill(); } catch { /* already gone */ } };
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(1); });

let failed = 0;
try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  for (const sc of SCENARIOS) {
    if (ONLY && sc.name !== ONLY) continue;
    console.log(`[mirror-scroll] ${sc.name}: turns=${sc.turns} images=${sc.images} imgdelay=${sc.imgdelay} mermaid=${sc.mermaid}${sc.from ? " (reusing a pane)" : ""}`);
    const rs = await runScenario(sc, chrome);
    failed += rs.filter((r) => !r.ok).length;
  }
} finally {
  cleanup();
}
console.log(failed ? `\n[mirror-scroll] FAILED: ${failed} run(s) did not land at the bottom` : "\n[mirror-scroll] OK");
process.exit(failed ? 1 : 0);
