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
//
// mode は検証する筋:
//   land（既定） — 開いたら末尾に着地する（＋読者の操作を追わない／ホイールで追従が切れる）
//   restore      — 途中まで読んで離れ、戻ってくると同じ位置に戻る（scrollMark）
//   swipe        — スマホの横スワイプでセッションを持ち替えると末尾に着地する。指の
//                  pointerdown が「読者が広げた」窓（noteInteraction）を武装したまま持ち越すと
//                  遅延レイアウトの再ピンが握りつぶされる、という筋を塞いだうえでの契約テスト。
//                  なお、この窓を持ち越したままのビルドでもここでは末尾に着地した（fetch と
//                  レンダが 600ms より長く、窓が閉じたあとの成長で再ピンが効くため）— つまり
//                  これは「不定にならないこと」を固定する検査であって、赤くなる再現ではない。
const SCENARIOS = [
  { name: "long", turns: 200, images: 3, imgdelay: 3000, mermaid: 0 },
  { name: "mermaid", turns: 12, images: 0, imgdelay: 0, mermaid: 3 },
  { name: "switch", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, from: "sc9lm3d" },
  { name: "restore", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, mode: "restore" },
  { name: "swipe", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, mode: "swipe", from: "sk4rq2f" },
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
    // 「最新へ」と「返信を頭から」は同じピル（.mirror-jump）なので、後者を除いて数える。
    jump: !!document.querySelector(".mirror-jump:not(.mirror-jump-top)"),
    replyTop: !!document.querySelector(".mirror-jump-top"),
    // どのセッションの transcript が載っているか — stub は sk4rq2f 以外の idx を +1000 する。
    first: Number(el.querySelector("[data-turn-idx]")?.dataset.turnIdx ?? -1),
    // 画面の一番上に見えているターンとそのズレ（scrollMark と同じ採り方）。位置の復元は px では
    // なく「どの内容が上端にあるか」で見る — 上にある 100 数十ターンの高さは訪問のたびに数 px
    // ずつ揺れる（ハイライトやフォント）ので、scrollTop の一致を求めると健全な復元まで落ちる。
    anchor: (() => {
      const t = el.getBoundingClientRect().top;
      for (const d of el.querySelectorAll("[data-turn-idx]")) {
        const r = d.getBoundingClientRect();
        if (r.bottom > t + 1) return { idx: Number(d.dataset.turnIdx), off: Math.round(r.top - t) };
      }
      return null;
    })(),
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
const openRow = (title) => `(() => {
  const b = [...document.querySelectorAll(".sess-row .sess-btn")]
    .find((e) => (e.textContent || "").includes(${JSON.stringify(title)}));
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;
const SESSION_A = "チェックアウトの入力検証"; // sk4rq2f — idx はそのまま（stub）
const SESSION_B = "返金 API の契約整理"; //     sc9lm3d — idx は +1000（stub）
const OPEN_SESSION = openRow(SESSION_A);
// 横スワイプの起点。ターンの見出し行を選ぶ — transcript の中（＝指の pointerdown が
// 「読者が広げた」窓を武装する場所）で、かつ横スクロールを持つ要素（コードブロック）でない
// ので swipeGuard に弾かれない。左 1/3 は drawer の領分なので右寄りを取る。
const SWIPE_FROM = `(() => {
  const body = document.querySelector(".mirror-body");
  if (!body) return "none";
  const br = body.getBoundingClientRect();
  return JSON.stringify({ x: Math.round(br.x + br.width * 0.7), y: Math.round(br.y + br.height / 2) });
})()`;

const pane = (session) => ({ id: "p0", session, content: { kind: "terminal", chat: true }, wrap: null });
const layout = (session) => ({ cols: [{ id: "c0", rowRatio: 0.5, panes: [pane(session)] }], colRatios: [1], activeId: "p0" });

// --- モード別の筋 ------------------------------------------------------------------

// land（既定）: 開いたら末尾。読者が広げた reflow は追わない。ホイールで追従が切れる。
async function runLanding(cdp) {
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
  // 末尾に貼り付いている間は「返信を頭から」を出さない — そこは引き継ぎカードの起動ボタンや
  // 質問カードの回答ボタンが並ぶ面で、浮くピルが被ると押せなくなる（実機で報告あり）。
  const ok = !!landed && landed.gap <= 2 && !landed.jump && !landed.replyTop && keptPlace && up.gap > 2 && up.jump && stayedPut;
  return {
    ok,
    note: `landed gap=${landed?.gap}px (turns=${landed?.turns}) replyTop=${landed?.replyTop}  expand-kept-place=${keptPlace}  wheel-up: gap=${up.gap}px jump=${up.jump} stayedPut=${stayedPut}`,
  };
}

// restore: A を途中まで読む → B へ持ち替える → A へ戻ると同じ位置（scrollMark）。ついでに
// 「B は B で末尾に着地する」＝マークがセッションを跨いで漏れないことも見る。
async function runRestore(cdp) {
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not find the session row in the left pane");
  await sleep(9000);
  const { x, y } = JSON.parse(await cdp.ev(WHEEL_UP));
  for (let i = 0; i < 4; i++) {
    await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: -400, pointerType: "mouse" });
    await sleep(80);
  }
  await sleep(1500);
  const before = await cdp.ev(PROBE); // 「途中まで読んだ」位置

  if ((await cdp.ev(openRow(SESSION_B))) !== "ok") throw new Error("could not find the second session row");
  await sleep(8000);
  const other = await cdp.ev(PROBE); // 別セッションは自分の末尾に着地する

  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not re-open the first session");
  await sleep(9000);
  const back = await cdp.ev(PROBE);
  await sleep(2500);
  const settled = await cdp.ev(PROBE); // 遅延レイアウトが片付いても居座っているか

  const sameSession = back.first === before.first && other.first !== before.first;
  // 同じターンが同じズレで上端に来ていれば復元できている（px の一致は求めない — 上記 PROBE）。
  const restored =
    !!before.anchor && !!back.anchor &&
    back.anchor.idx === before.anchor.idx &&
    Math.abs(back.anchor.off - before.anchor.off) <= 4;
  const stayed = Math.abs(settled.top - back.top) <= 5;
  const ok = before.top > 0 && sameSession && restored && stayed && back.jump && other.gap <= 2;
  return {
    ok,
    note: `left on turn ${before.anchor?.idx}@${before.anchor?.off}px → came back to ${back.anchor?.idx}@${back.anchor?.off}px (stayed=${stayed}, jump=${back.jump})  other-session landed gap=${other.gap}px`,
  };
}

// swipe: スマホの横スワイプでセッションを持ち替える。指が transcript の上に降りているのが
// 肝で、その pointerdown が持ち越されると再ピンが止まり、着地位置が不定になっていた。
async function runSwipe(cdp) {
  await sleep(6000); // 先頭のセッション（sc.from）が載って落ち着くまで
  const before = await cdp.ev(PROBE);
  const at = await cdp.ev(SWIPE_FROM);
  if (at === "none") throw new Error("no turn head to start the swipe from");
  const { x, y } = JSON.parse(at);
  // ← へ送る。縦は動かさない＝縦優先の見送り（|dx| <= |dy|）に落ちない。
  //
  // 刻みが大きく速いのは意図的: Chromium は連続した touchMove を合成して間の座標を返すので、
  // 細かく送ると 1 イベントぶんの dx が縮む。ROTATE_DIST=70 を LONG_PRESS_MS=500ms 以内に
  // 越えられないと、指を置いたまま考えている扱いで候補が取り消される（実測: 120ms 刻みだと
  // 70px を越えるのが 600ms 目で、スワイプが成立しなかった）。
  await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
  for (const dx of [-120, -240]) {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: x + dx, y }] });
    await sleep(20);
  }
  await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });

  await sleep(9000); // 持ち替え先の transcript + 遅延レイアウト
  const landed = await cdp.ev(PROBE);
  await sleep(2500);
  const settled = await cdp.ev(PROBE);

  // …そして、少しだけ上へ送ると「返信を頭から」が出る（＝末尾でだけ引っ込む導線であって、
  // 消えたわけではないことの確認）。回答 1 本ぶんより小さく送るのがポイント — 大きく送ると
  // 最新の回答そのものより上へ出てしまい、頭出しの対象が画面の下になる。
  const { x: wx, y: wy } = JSON.parse(await cdp.ev(WHEEL_UP));
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: wx, y: wy, deltaX: 0, deltaY: -300, pointerType: "mouse" });
  await sleep(1200);
  const up = await cdp.ev(PROBE);

  const rotated = !!landed && landed.first !== before.first;
  const ok =
    rotated && landed.gap <= 2 && !landed.jump && !landed.replyTop &&
    Math.abs(settled.top - landed.top) <= 5 && settled.gap <= 2 && up.replyTop;
  return {
    ok,
    note: `rotated=${rotated} (first ${before?.first}→${landed?.first})  landed gap=${landed?.gap}px replyTop=${landed?.replyTop}  settled gap=${settled?.gap}px  after small wheel-up: replyTop=${up?.replyTop}`,
  };
}

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
      // swipe の筋だけスマホ相当（≤760px = 左ペインがオフキャンバス drawer になる幅）＋タッチ。
      const phone = sc.mode === "swipe";
      await cdp.send("Emulation.setDeviceMetricsOverride",
        phone ? { width: 390, height: 844, deviceScaleFactor: 2, mobile: true }
              : { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
      if (phone) await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 });
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

      const r = sc.mode === "restore" ? await runRestore(cdp) : sc.mode === "swipe" ? await runSwipe(cdp) : await runLanding(cdp);
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
console.log(failed ? `\n[mirror-scroll] FAILED: ${failed} run(s) did not land where they should` : "\n[mirror-scroll] OK");
process.exit(failed ? 1 : 0);
