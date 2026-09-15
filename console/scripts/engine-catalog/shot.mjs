// Screenshots of the model-catalogue screens (ADR 0085 P2), driven headless against stub.mjs.
//
// Raw CDP over Node's global WebSocket, like scripts/shots/capture.mjs — no Playwright. The CP
// half of ADR 0085 is not merged, so this is the only way to see these screens at all; when it
// is, the same shots are taken against a real deployment and compared.
//
//   npm --prefix console run build
//   node console/scripts/engine-catalog/shot.mjs [--out /tmp] [--scene ledger,plan]
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8801));
const CDP_PORT = Number(arg("cdp-port", 9261));
const OUT = path.resolve(arg("out", "/tmp"));
const LOCALE = arg("locale", "ja");
const THEME = arg("theme", "dark");
const ONLY = arg("scene", "");
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const pane = (view) => ({
  cols: [{ id: "c0", rowRatio: 0.5, panes: [{ id: "p0", session: null, wrap: null, content: { kind: "engineAdd", engineKey: "image", lora: false, view } }] }],
  colRatios: [1],
  activeId: "p0",
});

const SCENES = [
  // The bucket under the rows: the af-sandbox state that had no screen (ADR 0085 decision 8).
  { name: "ledger", view: "registered", width: 1500, height: 1080 },
  // One press, priced: the plan card over the search tab (decision 4).
  {
    name: "plan", view: "search", width: 1500, height: 1080,
    action: `(() => {
      const add = Array.from(document.querySelectorAll('button'))
        .find((b) => (b.getAttribute('aria-label') || '').startsWith('追加: Anima'));
      add?.click();
      return !!add;
    })()`,
    settle: 2500,
  },
  // The only question this screen ever asks: which of the bucket's files fills a role, asked FOR
  // a checkpoint (decision 3).
  {
    name: "complete", view: "registered", width: 1500, height: 1080,
    action: `(() => {
      const align = document.querySelector('button[aria-label="揃える: anima-aesthetic-v1.1"]');
      align?.click();
      return !!align;
    })()`,
    settle: 2500,
  },
];

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (p) {
        this.pending.delete(msg.id);
        msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
      }
    });
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
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(url, init);
      if (r.ok) return await r.json();
    } catch { /* not up yet */ }
    await sleep(250);
  }
  throw new Error(`timeout waiting for ${url}`);
}

const stub = spawn(process.execPath, [path.join(HERE, "stub.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "inherit", "inherit"],
});
const chrome = spawn("/usr/bin/chromium", [
  "--headless=new",
  "--no-sandbox",
  "--disable-gpu",
  "--hide-scrollbars",
  "--font-render-hinting=none",
  `--remote-debugging-port=${CDP_PORT}`,
  "--remote-allow-origins=*",
  // 🔴 Headless reports a COARSE pointer, so `@media (hover: none)` applies and hover-only
  // controls are drawn on every row — a picture busier than the screen anybody sees.
  "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4",
  `--lang=${LOCALE === "ja" ? "ja-JP" : "en-US"}`,
  "about:blank",
], { stdio: ["ignore", "ignore", "ignore"] });

const cleanup = () => {
  try { chrome.kill(); } catch { /* already gone */ }
  try { stub.kill(); } catch { /* already gone */ }
};
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(1); });

try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  await fetchJSON(`${BASE}api/admin/engines`);
  for (const scene of SCENES.filter((s) => !ONLY || ONLY.split(",").includes(s.name))) {
    const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
    const cdp = await CDP.connect(target.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    await cdp.send("Emulation.setDeviceMetricsOverride", { width: scene.width, height: scene.height, deviceScaleFactor: 2, mobile: false });
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `try {
        localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE, theme: THEME }))});
        localStorage.setItem("af-tenant", "demo");
        localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(pane(scene.view)))});
      } catch (e) {}`,
    });
    await cdp.send("Page.navigate", { url: BASE });
    await sleep(4500);
    if (scene.action) {
      const ran = await cdp.send("Runtime.evaluate", { expression: scene.action, returnByValue: true });
      if (ran.result?.value !== true) throw new Error(`[${scene.name}] the action found nothing to press`);
      await sleep(scene.settle || 800);
    }
    // WebP straight out of Chromium, like scripts/shots/capture.mjs: a fifth of the PNG at a
    // quality where 2x UI text stays crisp, which is what makes these cheap to attach to a PR.
    const shot = await cdp.send("Page.captureScreenshot", { format: "webp", quality: 82, captureBeyondViewport: false });
    const file = path.join(OUT, `engine-catalog-${scene.name}-${LOCALE}.webp`);
    fs.mkdirSync(OUT, { recursive: true });
    fs.writeFileSync(file, Buffer.from(shot.data, "base64"));
    console.log("[shot]", file, `${scene.width}x${scene.height}@2x`);
    cdp.ws.close();
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
  }
} finally {
  cleanup();
}
