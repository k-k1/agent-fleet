// Does the file viewer come back to the same position after a trip to another tab?
//
// Runs the real Console bundle (served by stub.mjs) in headless Chromium and drives CDP over a
// plain WebSocket — no Playwright or Puppeteer, the same technique as the neighbouring
// mirror-scroll check. Two reasons jsdom cannot see this:
//   - The height settles late. The Markdown preview writes innerHTML in a passive effect and
//     then grows again when highlighting lands, so a build that restores the position once
//     fails here.
//   - Switching tabs and `hidden` are different paths. Tab layout paints only the selected
//     view, so the surface is unmounted; view/edit only sets display:none on it, and the
//     browser drops scrollTop to 0. Both must come back to the same position.
//
//   npm --prefix console run build       # console/dist (the real bundle) is required
//   npm --prefix console run viewer:scroll
//   node console/scripts/viewer-scroll/check.mjs --runs 3 --scenario markdown
//
// The exit status is the verdict (0 = every scenario returned to the same position).
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8793));
const CDP_PORT = Number(arg("cdp-port", 9253));
const RUNS = Number(arg("runs", 2));
const ONLY = arg("scenario", "");
const CPU = Number(arg("cpu", 4)); // throttle the main thread (same reason as mirror-scroll)
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
/** Tolerance on the restored position. Moving even one line height is not "the same position". */
const TOL = 4;

// mode:
//   tab   — go to another tab and back (the surface unmounts, refetches and is rebuilt)
//   modes — go view <-> edit and back (the surface stays alive and gets display:none). This
//           path stays green even on a build with the position memory removed, because
//           Chromium preserves scrollTop across display:none (measured). It is kept to pin the
//           contract, not to reproduce a regression; the two tab scenarios are what catch one
//           (the "positive control" in the README).
const SCENARIOS = [
  { name: "code", files: ["repos/shop/a.go", "repos/shop/b.go"], scroller: ".codeview", mode: "tab" },
  { name: "markdown", files: ["repos/shop/a.md", "repos/shop/b.md"], scroller: ".md-scroll", mode: "tab" },
  // PDF gets its own scenario: that surface moves the scroll position itself, for the in-page
  // anchor it keeps when the zoom changes.
  { name: "pdf", files: ["repos/shop/a.pdf", "repos/shop/b.pdf"], scroller: ".pdfview-scroll", mode: "tab" },
  { name: "modes", files: ["repos/shop/a.go", "repos/shop/b.go"], scroller: ".codeview", mode: "modes", editable: true },
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

// The surface's position and what is currently loaded in it. Which file it is matters too:
// landing on the wrong tab shows a different file at the same position and would go unnoticed.
const probe = (scroller) => `(() => {
  const el = document.querySelector(${JSON.stringify(scroller)});
  if (!el) return null;
  return {
    top: Math.round(el.scrollTop),
    max: Math.round(el.scrollHeight - el.clientHeight),
    file: (document.querySelector(".fi-path") || {}).textContent || "",
    tabs: document.querySelectorAll(".pane-tab").length,
    selected: [...document.querySelectorAll(".pane-tab")].findIndex((t) => t.classList.contains("selected")),
  };
})()`;
const center = (scroller) => `(() => {
  const el = document.querySelector(${JSON.stringify(scroller)});
  if (!el) return "none";
  const r = el.getBoundingClientRect();
  return JSON.stringify({ x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) });
})()`;
// The tab itself is .pane-tab, the container holding the context menu; the clickable element is
// the button[role=tab] inside it.
const clickTab = (i) => `(() => {
  const t = document.querySelectorAll('.pane-tab button[role="tab"]')[${i}];
  if (!t) return "no-tab";
  t.click();
  return "ok";
})()`;
// View <-> edit (the tablist in FileEditControls) — the path where the reading surface becomes
// hidden.
const clickMode = (i) => `(() => {
  const b = document.querySelectorAll(".file-mode-tabs button")[${i}];
  if (!b) return "no-tab";
  b.click();
  return "ok";
})()`;

const view = (id, filePath) => ({ id, session: null, content: { kind: "file", filePath }, wrap: null });
const layout = (files) => ({
  version: 3,
  mode: "tabs",
  cols: [{ id: "c0", rowRatio: 0.5, cells: [{ id: "cell0", selectedViewId: "v0", views: files.map((f, i) => view("v" + i, f)) }] }],
  colRatios: [1],
  activeCellId: "cell0",
});

/** Wait for the surface to appear. Not a fixed sleep: an editable file also boots CodeMirror,
 *  and with the main thread throttled 4x, 5 seconds is sometimes not enough (measured: only the
 *  modes scenario failed, reporting that the surface had not appeared). */
async function waitFor(cdp, scroller, ms = 20000) {
  for (let waited = 0; waited < ms; waited += 250) {
    const seen = await cdp.ev(`!!document.querySelector(${JSON.stringify(scroller)})`);
    if (seen) return true;
    await sleep(250);
  }
  return false;
}

/** Scroll the way a reader does. A real wheel event, not an assignment to scrollTop: restoration
 *  is abandoned on "was there input?", so the contract is only exercised through the input path. */
async function wheelDown(cdp, scroller, times = 6) {
  const at = await cdp.ev(center(scroller));
  if (at === "none") throw new Error("scroller not found: " + scroller);
  const { x, y } = JSON.parse(at);
  for (let i = 0; i < times; i++) {
    await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: 400, pointerType: "mouse" });
    await sleep(60);
  }
  await sleep(400);
}

async function runScenario(sc) {
  const stub = spawn(
    process.execPath,
    [path.join(HERE, "stub.mjs"), "--port", String(PORT), "--editable", sc.editable ? "1" : "0"],
    { stdio: ["ignore", "ignore", "inherit"] },
  );
  try {
    await fetchJSON(`${BASE}api/whoami`);
    const results = [];
    for (let run = 0; run < RUNS; run++) {
      const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
      const cdp = await CDP.connect(target.webSocketDebuggerUrl);
      await cdp.send("Page.enable");
      await cdp.send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
      if (CPU > 1) await cdp.send("Emulation.setCPUThrottlingRate", { rate: CPU });
      // A returning user's browser: display settings (tab layout) and a layout with two file tabs.
      await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
        source: `try {
          localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark","paneLayout":"tabs"}');
          localStorage.setItem("af-tenant", "demo");
          localStorage.setItem("af.layout2.demo@example.com.demo.tabs", ${JSON.stringify(JSON.stringify(layout(sc.files)))});
        } catch (e) {}`,
      });
      await cdp.send("Page.navigate", { url: BASE });
      await sleep(3000); // boot + first poll round; the surface appears after this (waitFor)

      const r = sc.mode === "modes" ? await runModes(cdp, sc) : await runTab(cdp, sc);
      results.push(r);
      console.log(`  [${sc.name} ${run + 1}/${RUNS}] ${r.ok ? "OK " : "NG "} ${r.note}`);
      cdp.ws.close();
      await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
    }
    return results;
  } finally {
    stub.kill();
    await sleep(300);
  }
}

/** Move to another tab and come back. */
async function runTab(cdp, sc) {
  await waitFor(cdp, sc.scroller);
  const opened = await cdp.ev(probe(sc.scroller));
  if (!opened) return { ok: false, note: `the surface did not appear (${sc.scroller})` };
  if (opened.tabs !== sc.files.length) return { ok: false, note: `${opened.tabs} tabs (expected ${sc.files.length})` };

  await wheelDown(cdp, sc.scroller);
  const read = await cdp.ev(probe(sc.scroller));
  if (!(read.top > 100)) return { ok: false, note: `did not scroll: top=${read.top} max=${read.max}` };

  if ((await cdp.ev(clickTab(1))) !== "ok") return { ok: false, note: "the second tab cannot be clicked" };
  await sleep(1500);
  const other = await cdp.ev(probe(sc.scroller));
  if (other && other.file === read.file) return { ok: false, note: "still the same file after clicking the tab" };

  if ((await cdp.ev(clickTab(0))) !== "ok") return { ok: false, note: "the first tab cannot be clicked" };
  await waitFor(cdp, sc.scroller);
  await sleep(2000);
  const back = await cdp.ev(probe(sc.scroller));
  await sleep(2000);
  const still = await cdp.ev(probe(sc.scroller)); // has a late height increase dragged it away?

  if (!back) return { ok: false, note: "the surface did not appear after coming back" };
  if (back.file !== read.file) return { ok: false, note: `came back to a different file: ${back.file}` };
  const ok = Math.abs(back.top - read.top) <= TOL && Math.abs(still.top - read.top) <= TOL;
  return { ok, note: `read at ${read.top} -> back at ${back.top} -> after 2s ${still.top}` };
}

/** Go view <-> edit and back (the reading surface is not unmounted, only display:none). */
async function runModes(cdp, sc) {
  await waitFor(cdp, sc.scroller);
  const opened = await cdp.ev(probe(sc.scroller));
  if (!opened) return { ok: false, note: `the surface did not appear (${sc.scroller})` };

  await wheelDown(cdp, sc.scroller);
  const read = await cdp.ev(probe(sc.scroller));
  if (!(read.top > 100)) return { ok: false, note: `did not scroll: top=${read.top} max=${read.max}` };

  if ((await cdp.ev(clickMode(1))) !== "ok") return { ok: false, note: "no edit tab (editable had no effect)" };
  await sleep(1200);
  const hidden = await cdp.ev(`(() => {
    const shell = document.querySelector(".file-viewer-shell");
    return shell ? shell.hasAttribute("hidden") : null;
  })()`);
  if (hidden !== true) return { ok: false, note: "switching to edit did not hide the reading surface (premise broken)" };

  if ((await cdp.ev(clickMode(0))) !== "ok") return { ok: false, note: "the view tab cannot be clicked" };
  await sleep(1500);
  const back = await cdp.ev(probe(sc.scroller));
  const ok = !!back && Math.abs(back.top - read.top) <= TOL;
  return { ok, note: `read at ${read.top} -> edit -> view ${back && back.top}` };
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
    console.log(`[viewer-scroll] ${sc.name}: ${sc.mode} ${sc.files.join(" / ")}`);
    const rs = await runScenario(sc);
    failed += rs.filter((r) => !r.ok).length;
  }
} finally {
  cleanup();
}
console.log(failed ? `\n[viewer-scroll] FAILED: ${failed} run(s) lost the position` : "\n[viewer-scroll] OK");
process.exit(failed ? 1 : 0);
