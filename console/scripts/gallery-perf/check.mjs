// Gallery perf harness (ADR 0080 follow-up, 2026-09-14): the three "feels slow" reports were
// never measured on the browser side — only the Agent's own thumbnail cost was (fs_thumb.go).
// This drives the real console bundle in headless Chromium (raw CDP, no Playwright) against
// stub.mjs, which answers `api/fs/tree` from a REAL folder on disk and reproduces fs_thumb.go's
// measured concurrency/latency shape (thumbSem=4, 95ms cold / ~0 cached) for `api/fs/download`.
//
//   npm --prefix console run build
//   node console/scripts/gallery-perf/check.mjs [--case folders|images|scroll] [--warm 1]
import { spawn } from "node:child_process";
import path from "node:path";
import os from "node:os";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8797));
const CDP_PORT = Number(arg("cdp-port", 9257));
const CASE = arg("case", "images"); // folders | images | scroll
const WARM = arg("warm", "1") === "1";
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Real folders under this container's home (shared across sessions — see workspace-notes.md).
const GENERATED_ROOT = ".cache/agent-fleet/generated"; // 6 session folders (issue 1: folder list)
const BIG_FOLDER = ".cache/agent-fleet/generated/4e6f8380-b58a-5cf5-bd34-113637da5472"; // 202 images

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.reqs = new Map(); // requestId -> {url, t0, tEnd}
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.method === "Network.requestWillBeSent") {
        const url = msg.params?.request?.url || "";
        if (url.includes("/api/fs/download") || url.includes("/api/fs/tree")) {
          this.reqs.set(msg.params.requestId, { url, t0: Date.now() });
        }
        return;
      }
      if (msg.method === "Network.loadingFinished") {
        const r = this.reqs.get(msg.params.requestId);
        if (r) r.tEnd = Date.now();
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
    const r = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  }
  thumbReqs() {
    return [...this.reqs.values()].filter((r) => r.url.includes("thumb="));
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

function paneFor(galleryPath) {
  const pane = { id: "p0", session: null, content: { kind: "gallery", galleryPath, sort: "new" }, wrap: null };
  return { cols: [{ id: "c0", rowRatio: 1, panes: [pane] }], colRatios: [1], activeId: "p0" };
}

// Issue 1: how long does the grid show ONLY the "Up" card (no folders/images yet) after a
// navigation, and what replaces that window with the fix. `.gal-card` covers Up + every real
// card, so a count of exactly 1 with no `.gal-card.image`/extra `.gal-card.folder` beyond Up
// is the bare state; a `.ui-empty` with the loading icon is the fixed one.
async function caseFolders(cdp, t0) {
  let sawLoading = -1;
  let sawCards = -1;
  for (let i = 0; i < 400; i++) {
    const r = await cdp.ev(`(() => ({
      cards: document.querySelectorAll(".gal-card").length,
      folders: document.querySelectorAll(".gal-card.folder").length,
      loading: !!document.querySelector(".ui-empty .codicon-loading"),
    }))()`);
    if (r.loading && sawLoading < 0) sawLoading = Date.now() - t0;
    // "real" content: more than just the Up card (folders>1 counts Up itself as a folder card)
    if (r.folders > 1 && sawCards < 0) sawCards = Date.now() - t0;
    if (sawCards >= 0) break;
    await sleep(10);
  }
  const treeReq = [...cdp.reqs.values()].find((r) => r.url.includes("/api/fs/tree"));
  const netMs = treeReq?.tEnd ? treeReq.tEnd - treeReq.t0 : -1;
  return { sawLoading, sawCards, netMs };
}

// Issues 2/3: how many thumbnail requests fire before any scroll (all-at-once?), and once the
// visible set changes (scroll to bottom), how long until the newly-visible cards' thumbnails are
// requested and finished, and how many OTHER thumbnails were still competing for a connection.
async function caseImages(cdp, doScroll) {
  if (!doScroll) {
    await sleep(9000); // let the grid mount and any lazy/observer decisions settle
    const before = cdp.thumbReqs().length;
    const beforeUnfinished = cdp.thumbReqs().filter((r) => !r.tEnd).length;
    return { atRest: before, atRestUnfinished: beforeUnfinished };
  }

  // The real complaint is a scroll made WHILE the initial batch is still in flight, not one made
  // after everything has quietly drained (9s at rest was enough time for the whole folder to
  // finish downloading once already). So: wait only for the grid to MOUNT (boot + one fs/tree
  // round trip, ~1.2s measured separately), then scroll immediately — a folder of 200 takes
  // thumbSem=4 x ~95ms cold per wave to drain, so an impatient scroll right after mount is a
  // completely ordinary thing to do, not an edge case.
  for (let i = 0; i < 400; i++) {
    if ((await cdp.ev(`document.querySelectorAll(".gal-card.image").length`)) > 0) break;
    await sleep(10);
  }
  const names = await cdp.ev(`(() => Array.from(document.querySelectorAll(".gal-card.image .gal-name")).slice(-6).map(e => e.title))()`);
  await cdp.ev(`(() => { const b = document.querySelector(".gal-body"); if (b) b.scrollTop = b.scrollHeight; })()`);
  const tScroll = Date.now();
  // Wait for the last-row images to finish, or time out.
  let doneAt = -1;
  for (let i = 0; i < 200; i++) {
    const loaded = await cdp.ev(`(() => Array.from(document.querySelectorAll(".gal-card.image")).slice(-6)
      .filter(c => { const img = c.querySelector('.gal-thumb img'); return img && img.complete && img.naturalWidth > 0; }).length)()`);
    if (loaded >= Math.min(6, names.length)) {
      doneAt = Date.now() - tScroll;
      break;
    }
    await sleep(25);
  }
  const atScroll = cdp.thumbReqs().filter((r) => r.t0 <= tScroll);
  const after = cdp.thumbReqs();
  const inFlightAtScroll = atScroll.filter((r) => !r.tEnd || r.tEnd > tScroll).length;
  return {
    firedBeforeScroll: atScroll.length,
    scrollToVisibleMs: doneAt,
    inFlightAtScroll,
    totalThumbReqs: after.length,
  };
}

const stub = spawn(process.execPath, [path.join(HERE, "stub.mjs"), "--port", String(PORT)], { stdio: ["ignore", "inherit", "inherit"] });
const chrome = spawn(
  "/usr/bin/chromium",
  ["--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
   `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank"],
  { stdio: ["ignore", "ignore", "ignore"] },
);
const cleanup = () => {
  try { chrome.kill(); } catch { /* gone */ }
  try { stub.kill(); } catch { /* gone */ }
};
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(1); });

try {
  await fetchJSON(`${BASE}api/whoami`);
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
  const cdp = await CDP.connect(target.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Network.enable");
  await cdp.send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  const galleryPath = CASE === "folders" ? GENERATED_ROOT : BIG_FOLDER;
  const layout = paneFor(galleryPath);
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try {
      localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark"}');
      localStorage.setItem("af-tenant", "demo");
      localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout))});
    } catch (e) {}`,
  });
  const tNav = Date.now();
  await cdp.send("Page.navigate", { url: BASE });

  console.log(`[gallery-perf] case=${CASE} warm=${WARM} path=${galleryPath}`);
  if (CASE === "folders") {
    const r = await caseFolders(cdp, tNav);
    console.log(`  loading state visible at +${r.sawLoading}ms, real cards at +${r.sawCards}ms, api/fs/tree round-trip ${r.netMs}ms`);
  } else if (CASE === "scroll") {
    const r = await caseImages(cdp, true);
    console.log(
      `  scrolled to bottom as soon as the grid mounted (${r.firedBeforeScroll} thumb requests already fired by then)\n` +
      `  last row loaded ${r.scrollToVisibleMs < 0 ? "(timed out)" : `after ${r.scrollToVisibleMs}ms`}, ` +
      `${r.inFlightAtScroll} other thumb requests were in flight/queued at that moment, ` +
      `${r.totalThumbReqs} thumb requests total`,
    );
  } else {
    const r = await caseImages(cdp, false);
    console.log(`  at rest (9s, no scroll), 202 cards in DOM: ${r.atRest} thumb requests fired (${r.atRestUnfinished} still unfinished)`);
  }
  cdp.ws.close();
} finally {
  cleanup();
}
