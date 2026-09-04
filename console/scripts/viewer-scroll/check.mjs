// ファイルビュアーは、別のタブへ行って戻ってきたとき同じ位置に居るか。
//
// 本物の Console バンドル（stub.mjs が配る）を headless Chromium で動かし、CDP を素の
// WebSocket で叩く（Playwright / Puppeteer は入れない・隣の mirror-scroll と同じ技法）。
// jsdom では見えない検査である理由は 2 つ:
//   - **高さが遅れて確定する**。Markdown プレビューは innerHTML を passive effect で書き、
//     そのあとハイライトで伸びる。「戻す」を 1 回で済ませたビルドはここで落ちる。
//   - **タブ切替と `hidden` の違い**。タブ表示は選ばれた 1 枚しか描かない＝面ごと unmount、
//     表示⇄編集は面を display:none にするだけ（ブラウザが scrollTop を 0 に落とす）。
//     どちらも「戻ってきたら同じ位置」でなければならないが、経路が別物。
//
//   npm --prefix console run build       # console/dist（本物のバンドル）が要る
//   npm --prefix console run viewer:scroll
//   node console/scripts/viewer-scroll/check.mjs --runs 3 --scenario markdown
//
// 終了ステータスが検査結果（0 = 全シナリオが同じ位置に戻った）。
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
const CPU = Number(arg("cpu", 4)); // 主スレッドを絞る（mirror-scroll と同じ理由）
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
/** 戻り位置のずれの許容。行の高さ 1 つぶんも動いたら「同じ位置」ではない。 */
const TOL = 4;

// mode:
//   tab   — 別のタブへ行って戻る（＝面ごと unmount → 再取得 → 組み直し）
//   modes — 表示⇄編集を往復する（＝面は生きたまま display:none になる）。★この筋は
//           **位置の記憶を殺したビルドでも緑になる**（Chromium は display:none を跨いで
//           scrollTop を保つ・2026-09-04 実測）。契約を固定するために残しているだけで、
//           退行の再現ではない —— 捕まえているのは tab の 2 本（README の「正の対照」）。
const SCENARIOS = [
  { name: "code", files: ["repos/shop/a.go", "repos/shop/b.go"], scroller: ".codeview", mode: "tab" },
  { name: "markdown", files: ["repos/shop/a.md", "repos/shop/b.md"], scroller: ".md-scroll", mode: "tab" },
  // PDF は面が自前でスクロール位置を触る（倍率変更のページ内アンカー）ので別に見る。
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
    try { const r = await fetch(url, init); if (r.ok) return await r.json(); } catch { /* まだ上がっていない */ }
    await sleep(200);
  }
  throw new Error("timeout waiting for " + url);
}

// 面の位置と、いま何が載っているか。**どのファイルが載っているか**まで見るのは、
// タブを踏み外して「同じ位置のまま別のファイル」を見ても気づけないから。
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
// タブ本体は .pane-tab（右クリックメニューを持つ器）で、押せるのは中の button[role=tab]。
const clickTab = (i) => `(() => {
  const t = document.querySelectorAll('.pane-tab button[role="tab"]')[${i}];
  if (!t) return "no-tab";
  t.click();
  return "ok";
})()`;
// 表示⇄編集（FileEditControls の tablist）。「読む面が hidden になる」経路。
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

/** 面が出るまで待つ。**固定の sleep にしない**: 編集できるファイルでは CodeMirror も
 *  上がるので、主スレッドを 4 倍に絞った状態では 5 秒では間に合わないことがある
 *  （実測: modes の筋だけ「面が出ていない」で落ちた）。 */
async function waitFor(cdp, scroller, ms = 20000) {
  for (let waited = 0; waited < ms; waited += 250) {
    const seen = await cdp.ev(`!!document.querySelector(${JSON.stringify(scroller)})`);
    if (seen) return true;
    await sleep(250);
  }
  return false;
}

/** 読み手が実際に送る。scrollTop への代入ではなく本物のホイールを使う —— 復元の打ち切りは
 *  「入力があったか」で決めているので、入力の経路ごと通しておかないと契約を見たことにならない。 */
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
      // 戻ってきた利用者のブラウザ: 表示設定（タブ表示）と、2 枚のファイルタブを持つ配置。
      await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
        source: `try {
          localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark","paneLayout":"tabs"}');
          localStorage.setItem("af-tenant", "demo");
          localStorage.setItem("af.layout2.demo@example.com.demo.tabs", ${JSON.stringify(JSON.stringify(layout(sc.files)))});
        } catch (e) {}`,
      });
      await cdp.send("Page.navigate", { url: BASE });
      await sleep(3000); // ブート＋最初のポーリング（面が出るのはこのあと・waitFor で待つ）

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

/** 別のタブへ移って戻る。 */
async function runTab(cdp, sc) {
  await waitFor(cdp, sc.scroller);
  const opened = await cdp.ev(probe(sc.scroller));
  if (!opened) return { ok: false, note: `面が出ていない (${sc.scroller})` };
  if (opened.tabs !== sc.files.length) return { ok: false, note: `タブが ${opened.tabs} 枚（期待 ${sc.files.length}）` };

  await wheelDown(cdp, sc.scroller);
  const read = await cdp.ev(probe(sc.scroller));
  if (!(read.top > 100)) return { ok: false, note: `送れていない top=${read.top} max=${read.max}` };

  if ((await cdp.ev(clickTab(1))) !== "ok") return { ok: false, note: "2 枚目のタブが押せない" };
  await sleep(1500);
  const other = await cdp.ev(probe(sc.scroller));
  if (other && other.file === read.file) return { ok: false, note: "タブを押しても同じファイルのまま" };

  if ((await cdp.ev(clickTab(0))) !== "ok") return { ok: false, note: "1 枚目のタブが押せない" };
  await waitFor(cdp, sc.scroller);
  await sleep(2000);
  const back = await cdp.ev(probe(sc.scroller));
  await sleep(2000);
  const still = await cdp.ev(probe(sc.scroller)); // 遅れて伸びた高さに引きずられていないか

  if (!back) return { ok: false, note: "戻ってきた面が出ていない" };
  if (back.file !== read.file) return { ok: false, note: `戻り先が別のファイル: ${back.file}` };
  const ok = Math.abs(back.top - read.top) <= TOL && Math.abs(still.top - read.top) <= TOL;
  return { ok, note: `読んだ位置 ${read.top} → 戻り ${back.top} → 2 秒後 ${still.top}` };
}

/** 表示⇄編集を往復する（読む面は unmount されず display:none になる）。 */
async function runModes(cdp, sc) {
  await waitFor(cdp, sc.scroller);
  const opened = await cdp.ev(probe(sc.scroller));
  if (!opened) return { ok: false, note: `面が出ていない (${sc.scroller})` };

  await wheelDown(cdp, sc.scroller);
  const read = await cdp.ev(probe(sc.scroller));
  if (!(read.top > 100)) return { ok: false, note: `送れていない top=${read.top} max=${read.max}` };

  if ((await cdp.ev(clickMode(1))) !== "ok") return { ok: false, note: "編集タブが無い（editable が効いていない）" };
  await sleep(1200);
  const hidden = await cdp.ev(`(() => {
    const shell = document.querySelector(".file-viewer-shell");
    return shell ? shell.hasAttribute("hidden") : null;
  })()`);
  if (hidden !== true) return { ok: false, note: "編集にしても読む面が隠れていない（前提が崩れている）" };

  if ((await cdp.ev(clickMode(0))) !== "ok") return { ok: false, note: "表示タブが押せない" };
  await sleep(1500);
  const back = await cdp.ev(probe(sc.scroller));
  const ok = !!back && Math.abs(back.top - read.top) <= TOL;
  return { ok, note: `読んだ位置 ${read.top} → 編集 → 表示 ${back && back.top}` };
}

const chrome = spawn("/usr/bin/chromium", [
  "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
  `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank",
], { stdio: ["ignore", "ignore", "ignore"] });
const cleanup = () => { try { chrome.kill(); } catch { /* もう居ない */ } };
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
console.log(failed ? `\n[viewer-scroll] FAILED: ${failed} run(s) が位置を失った` : "\n[viewer-scroll] OK");
process.exit(failed ? 1 : 0);
