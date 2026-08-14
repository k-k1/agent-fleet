// Do a tabbed cell's header buttons land where a non-tabbed pane puts them?
//
// The regressions this guards (2026-08-14):
//  1. A tabbed cell does NOT render the floating .pane-controls cluster — popout/wrap/
//     close ride inside the selected view's own header (`headerActions`). Every header
//     still reserved the gutter that cluster would have needed (--pane-ctl-w, plus the
//     per-view extras in scm.css), so the buttons sat 65–212 px in from the pane edge.
//  2. .session-state carries `margin-left:auto` for the left-rail rows (sessions.css),
//     and the assistant-chat head is the one band that renders it as a DIRECT child —
//     two auto-margined children make flex split the free space EVENLY, stranding
//     待機中/作業計画 mid-row.
//
// The tabbed pane below carries BOTH `pane tabbed` and a .pane-tabs child, because
// that is what Pane.tsx renders: the tabbed rules key on the .tabbed class, not on
// :has(.pane-tabs) — a descendant :has() forces Chrome to re-test the subject on every
// mutation inside the pane, and the terminal's DOM renderer mutates its rows every
// frame (that cost 19 of 20 profiled seconds in Recalculate style). Drop `tabbed` here
// and every check below fails, reporting fault 1 — the class IS the contract now.
//
// All of these are pure cascade/geometry faults, so this needs a layout engine but no bundle,
// no CP and no agent: the page links the REAL stylesheets and measures in headless
// Chromium over raw CDP (Node's global WebSocket — no Playwright/Puppeteer, same
// technique as ../shots/capture.mjs).
//
//   npm --prefix console run panes:heads
//   node console/scripts/pane-heads/check.mjs --widths 480,700,1100 --screenshot /tmp/heads.png
//
// Exit status is the check: 0 = every header puts the cluster where the floating one
// sits, with no stray hole in the row.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { pathToFileURL } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.join(HERE, "../../src");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const CDP_PORT = Number(arg("cdp-port", 9257));
const CHROME = arg("chrome", process.env.CHROMIUM || "/usr/bin/chromium");
const WIDTHS = arg("widths", "480,700,1100").split(",").map(Number);
const ONLY = arg("only", "");
const SHOT = arg("screenshot", "");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- stylesheets
// Link EVERY stylesheet, never a hand-picked subset: the first cut of this harness
// omitted sessions.css and therefore reported the .session-state fault as absent.
// main.tsx's own CSS imports are ordered; the CSS reached through `import { App }`
// (line 4) executes BEFORE them, so those go first. Within that leading group the
// order is alphabetical rather than graph order — fine while every shared class has
// a single owner (see the .view-head note in ui/ui.css), and a duplicate would be a
// bug in its own right.
function stylesheets() {
  const main = fs.readFileSync(path.join(SRC, "app/main.tsx"), "utf8");
  const ordered = [...main.matchAll(/^import\s+"([^"]+\.css)";/gm)]
    .map((m) => m[1])
    .filter((p) => p.startsWith("."))
    .map((p) => path.resolve(SRC, "app", p));
  const all = [];
  const walk = (dir) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith(".css")) all.push(p);
    }
  };
  walk(SRC);
  const rest = all.filter((p) => !ordered.includes(p)).sort();
  const codicon = path.join(HERE, "../../node_modules/@vscode/codicons/dist/codicon.css");
  const list = [...(fs.existsSync(codicon) ? [codicon] : []), ...rest, ...ordered];
  return { list, missing: ordered.filter((p) => !fs.existsSync(p)) };
}

// ---------------------------------------------------------------------- views
// Header markup transcribed from each view's JSX, with `headerActions` where that
// view places it. This is a transcription, so it tracks the CASCADE, not the JSX:
// a button added to a header in .tsx will not appear here by itself (README).
const ACTIONS = `<span class="tab-pane-actions">
    <button type="button" class="ui-btn ui-btn-ghost ui-iconbtn"><span class="codicon codicon-link-external"></span></button>
    <button type="button" class="ui-btn ui-btn-ghost ui-iconbtn pane-close"><span class="codicon codicon-close"></span></button>
  </span>`;
const TOGGLE = `<div class="ui-seg sm mirror-toggle"><button class="seg-btn">チャット</button><button class="seg-btn active">ターミナル</button></div>`;
const SESSION_CHIP = `<span class="pane-session"><span class="kind-tag kind-claude"><span class="codicon codicon-hubot"></span>claude</span><span class="pane-session-name">wip-sy7itrh</span><span class="session-state on">待機中</span></span>`;

// Where the mirror/terminal head puts the cluster RELATIVE to the switch is a JSX
// decision, not a CSS one, so read it out of the source instead of transcribing it —
// putting the cluster back before the switch (the original bug) then shows up as a
// ~150px rightGap and fails, exactly as it should. Hard-fails rather than assuming a
// default: a silently-wrong transcription is how the first cut of this harness missed
// a real fault.
function switchOrder(file) {
  const src = fs.readFileSync(path.join(SRC, file), "utf8");
  const head = src.indexOf("<ViewHead");
  const seg = head < 0 ? "" : src.slice(head, head + 1500);
  const actions = seg.indexOf("{headerActions}");
  const toggle = seg.indexOf("<MirrorToggle");
  if (actions < 0 || toggle < 0) {
    console.error(`[pane-heads] cannot tell where ${file} puts headerActions relative to <MirrorToggle> — update this harness`);
    process.exit(2);
  }
  return actions < toggle ? ACTIONS + TOGGLE : TOGGLE + ACTIONS;
}

// kind → [view root class, header markup]. `head` must contain exactly one
// .tab-pane-actions; everything before it is that view's own header content.
const VIEWS = {
  // MirrorView.tsx / TerminalView.tsx — chip + the チャット/ターミナル switch.
  mirror: ["mirrorview", `<header class="view-head">${SESSION_CHIP}<span class="view-head-actions">${switchOrder("features/mirror/MirrorView.tsx")}</span></header>`],
  terminal: ["termview", `<header class="view-head view-head-term">${SESSION_CHIP}<span class="view-head-actions">${switchOrder("features/terminal/TerminalView.tsx")}</span></header>`],
  // ChatView.tsx — the one head that renders .session-state as a DIRECT child.
  chat: [
    "chatview",
    `<header class="view-head fileinfo"><span class="fi-name">何ができるの？</span>` +
      `<button class="kind-tag kind-claude chat-agent-pick">Claude</button>` +
      `<span class="session-state on">待機中</span>` +
      `<button class="chat-plan-toggle">作業計画</button>` +
      `<span class="view-head-actions">${ACTIONS}</span></header>`,
  ],
  // SourceControlView.tsx / ChangesView.tsx — own buttons, then the cluster.
  scm: [
    "scmview",
    `<header class="view-head"><span class="view-title">agent-fleet — develop</span>` +
      `<span class="view-head-actions"><button class="ui-btn ui-btn-ghost">変更</button><button class="ui-btn ui-btn-ghost">fetch</button>` +
      `<button class="ui-btn ui-btn-ghost">Fast-Forward</button><button class="ui-btn ui-btn-ghost ui-iconbtn">更新</button>${ACTIONS}</span></header>`,
  ],
  // FileView.tsx — .fi-end (path + download) is itself margin-left:auto'd.
  file: [
    "fileview",
    `<header class="view-head fileinfo"><span class="fi-name mono">panes.css</span><span class="fi-tag">CSS</span>` +
      `<span class="fi-meta muted">4.2 KB · 210 行</span>` +
      `<span class="fi-end"><span class="fi-path">console/src/features/panes/panes.css</span><a class="fi-dl"><span class="codicon codicon-desktop-download"></span></a></span>` +
      `<span class="view-head-actions">${ACTIONS}</span></header>`,
  ],
  // DocView.tsx / DiffView.tsx / ReaderView.tsx — title only.
  doc: ["docview", `<header class="view-head fileinfo"><span class="fi-name">作業計画</span><span class="view-head-actions">${ACTIONS}</span></header>`],
  // SharedSessionView.tsx — its own band, not ViewHead.
  shared: [
    "shared-view",
    `<header class="shared-view-head"><div class="shared-view-info"><div><strong>共有セッション</strong></div>` +
      `<small>k1.kami@example.com · 読み取り · running</small></div><span class="view-head-actions">${ACTIONS}</span></header>`,
  ],
  // BrowserPane.tsx / BrowserAttachPane.tsx — a toolbar, not ViewHead.
  browser: [
    "browserpane",
    `<form class="browser-toolbar"><span class="browser-port">5173</span><input class="browser-path" value="/">` +
      `<button class="ui-btn ui-btn-ghost ui-iconbtn"><span class="codicon codicon-refresh"></span></button>` +
      `<span class="view-head-actions">${ACTIONS}</span></form>`,
  ],
};

const page = (sheets, kinds) => `<!doctype html>
<meta charset="utf-8"><title>pane heads</title>
${sheets.map((p) => `<link rel="stylesheet" href="${pathToFileURL(p).href}">`).join("\n")}
<style>
  html, body { margin: 0; background: var(--bg, #111); }
  .host { position: relative; height: 108px; margin: 8px; }
  .host > .pane { position: absolute; inset: 0; }
  .host > .pane > .filler { flex: 1; }
</style>
<div id="stage">
${kinds
  .map(
    ([kind, [rootClass, head]]) => `<div class="host" data-kind="${kind}">
  <div class="pane tabbed active" style="--pane-ctl-n: 2">
    <div class="pane-tabs" role="tablist">
      <div class="pane-tab selected"><button type="button" role="tab"><span class="pane-tab-title">タブ1</span></button><button class="pane-tab-close">×</button></div>
      <div class="pane-tab"><button type="button" role="tab"><span class="pane-tab-title">タブ2</span></button><button class="pane-tab-close">×</button></div>
    </div>
    <button type="button" class="pane-grip pane-ord ord1">1</button>
    <div class="${rootClass}">${head}<div class="filler"></div></div>
  </div>
</div>`,
  )
  .join("\n")}
<!-- Reference: the SAME pane WITHOUT tabs, where the cluster floats. Its inset is
     what every tabbed header above has to reproduce — so the expected value is
     derived from the shipping non-tabbed rule, not hard-coded here. -->
<div class="host" data-kind="__reference__">
  <div class="pane active" style="--pane-ctl-n: 2">
    <button type="button" class="pane-grip pane-ord ord1">1</button>
    <div class="pane-controls">
      <button type="button" class="ui-btn ui-btn-ghost ui-iconbtn"><span class="codicon codicon-link-external"></span></button>
      <button type="button" class="ui-btn ui-btn-ghost ui-iconbtn pane-close"><span class="codicon codicon-close"></span></button>
    </div>
    <div class="mirrorview"><header class="view-head">${SESSION_CHIP}<span class="view-head-actions">${TOGGLE}</span></header><div class="filler"></div></div>
  </div>
</div>
</div>
`;

// Measured per header. `holes` are the gaps between consecutive header children. ONE
// big hole is the legitimate title→buttons gap — note it is not necessarily the last
// one, since the pinned group can be several elements wide (待機中/作業計画/cluster).
// TWO big holes is the signature of a second `margin-left:auto`: flex splits the free
// space evenly between the auto margins and strands whatever sits between them.
const PROBE = `(() => {
  const out = {};
  for (const host of document.querySelectorAll(".host")) {
    const pane = host.querySelector(".pane").getBoundingClientRect();
    const ref = host.querySelector(".pane-controls");
    const acts = host.querySelector(".tab-pane-actions") || ref;
    const head = host.querySelector(".view-head, .shared-view-head, .browser-toolbar");
    const box = acts.getBoundingClientRect();
    const headBox = head.getBoundingClientRect();
    const kids = [...head.children].map((c) => c.getBoundingClientRect()).filter((r) => r.width > 0);
    const holes = [];
    for (let i = 1; i < kids.length; i++) holes.push(Math.round(kids[i].left - kids[i - 1].right));
    out[host.dataset.kind] = {
      rightGap: Math.round(pane.right - box.right),
      holes,
      bigHoles: holes.filter((h) => h > 24).length,
      rowOffset: Math.round(box.top + box.height / 2 - headBox.top),
      underTabs: Math.round(box.top - pane.top) < 32 && !!host.querySelector(".pane-tabs"),
      clipped: Math.round(pane.right - box.right) < 0 || Math.round(box.left - pane.left) < 0,
    };
  }
  return out;
})()`;

// ------------------------------------------------------------------------ CDP
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => {
      ws.addEventListener("open", res, { once: true });
      ws.addEventListener("error", rej, { once: true });
    });
    return new CDP(ws);
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  async ev(expression) {
    const r = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "eval failed");
    return r.result.value;
  }
}

const fetchJSON = async (url) => {
  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(url);
      if (r.ok) return await r.json();
    } catch {
      /* not up yet */
    }
    await sleep(200);
  }
  throw new Error(`unreachable: ${url}`);
};

// ----------------------------------------------------------------------- main
const { list, missing } = stylesheets();
if (missing.length) {
  console.error(`[pane-heads] main.tsx imports a stylesheet that does not exist: ${missing.join(", ")}`);
  process.exit(2);
}
const kinds = Object.entries(VIEWS).filter(([k]) => !ONLY || k === ONLY);
if (!kinds.length) {
  console.error(`[pane-heads] unknown --only ${ONLY} (have: ${Object.keys(VIEWS).join(", ")})`);
  process.exit(2);
}
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pane-heads-"));
const html = path.join(dir, "harness.html");
fs.writeFileSync(html, page(list, kinds));

const chrome = spawn(
  CHROME,
  [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
    // The harness reads the repo's CSS off disk from a file:// page.
    "--allow-file-access-from-files",
    // Headless reports a coarse pointer by default, which flips the hover/pointer
    // media queries some header controls are gated on.
    "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4",
    `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank",
  ],
  { stdio: ["ignore", "ignore", "ignore"] },
);
const cleanup = () => {
  try {
    chrome.kill();
  } catch {
    /* already gone */
  }
  fs.rmSync(dir, { recursive: true, force: true });
};
process.on("exit", cleanup);
process.on("SIGINT", () => {
  cleanup();
  process.exit(1);
});

let failed = 0;
try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  const target = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" })).json();
  const cdp = await CDP.connect(target.webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Page.navigate", { url: pathToFileURL(html).href });
  await sleep(700);
  const hover = await cdp.ev(`matchMedia("(hover: hover)").matches`);
  if (!hover) console.warn("[pane-heads] warning: (hover: hover) is false — hover-gated controls will not render");

  for (const width of WIDTHS) {
    await cdp.send("Emulation.setDeviceMetricsOverride", {
      width, height: 140 * (kinds.length + 1) + 40, deviceScaleFactor: 1, mobile: false,
    });
    await sleep(250);
    const m = await cdp.ev(PROBE);
    const ref = m.__reference__;
    if (!ref) throw new Error("reference pane not measured");
    console.log(`\n[pane-heads] width=${width}px  reference(floating cluster)=${ref.rightGap}px from the pane edge`);
    for (const [kind] of kinds) {
      const r = m[kind];
      // 1) No reserved gutter: the cluster must sit as close to the edge as the
      //    floating one, give or take a header's own inset (.shared-view-head uses 16px).
      const inset = r.rightGap <= ref.rightGap + 12;
      // 2) One auto margin per band: exactly one big hole (title → the pinned group).
      const packed = r.bigHoles <= 1;
      // 3) Still on the header's first row, and below the tab strip rather than under it.
      const onRow = r.rowOffset <= 44 && !r.underTabs && !r.clipped;
      const ok = inset && packed && onRow;
      if (!ok) failed++;
      console.log(
        `  ${kind.padEnd(9)} ${ok ? "OK " : "NG "} rightGap=${String(r.rightGap).padStart(4)}px` +
          `${inset ? "" : ` (>${ref.rightGap + 12}px — reserved gutter?)`}` +
          `  holes=[${r.holes.join(",")}]${packed ? "" : " (2 big holes — a second margin-left:auto?)"}` +
          `  rowOffset=${String(r.rowOffset).padStart(3)}px${onRow ? "" : " (wrapped / under the tab strip)"}`,
      );
    }
  }
  if (SHOT) {
    await cdp.send("Emulation.setDeviceMetricsOverride", {
      width: Number(arg("shot-width", 1000)), height: 140 * (kinds.length + 1) + 40, deviceScaleFactor: 1, mobile: false,
    });
    await sleep(200);
    const { data } = await cdp.send("Page.captureScreenshot", { format: "png" });
    fs.writeFileSync(SHOT, Buffer.from(data, "base64"));
    console.log(`\n[pane-heads] screenshot → ${SHOT}`);
  }
  cdp.ws.close();
  await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
} finally {
  cleanup();
}
console.log(failed ? `\n[pane-heads] FAILED: ${failed} check(s)` : "\n[pane-heads] OK");
process.exit(failed ? 1 : 0);
