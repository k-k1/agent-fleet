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
// mode selects which behaviour is checked:
//   land (default) - opening lands at the bottom (and does not follow the reader's own growth;
//                    a wheel breaks the follow)
//   restore        - read partway, leave, come back to the same place (scrollMark)
//   swipe          - a horizontal phone swipe that switches sessions lands at the bottom. This is
//                    a contract test written after closing the path where the finger's pointerdown
//                    carries the "the reader expanded this" window (noteInteraction) over still
//                    armed, which swallows the re-pin for late layout. Note that even a build that
//                    carries the window over lands at the bottom here (fetch plus render take
//                    longer than 600ms, so the growth after the window closes still re-pins), so
//                    this pins down "it must not be nondeterministic" rather than reproducing a
//                    red.
//   shared         - the same two points on a shared session, receiving side (docs/log/59): the
//                    bottom on first open, and the same place after leaving and returning. The
//                    render layer is shared with the mirror, but landing and restoring belong to
//                    SharedSessionView itself, so they are checked separately.
//   typing         - stay pinned to the bottom while writing in the composer. Even with the input
//                    grown tall, the transcript must not float off the bottom on each keystroke
//                    (measured: 154px from one keystroke). Checked with scroll anchoring disabled
//                    (see the note on runTyping).
const SCENARIOS = [
  { name: "long", turns: 200, images: 3, imgdelay: 3000, mermaid: 0 },
  { name: "mermaid", turns: 12, images: 0, imgdelay: 0, mermaid: 3 },
  { name: "switch", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, from: "sc9lm3d" },
  { name: "restore", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, mode: "restore" },
  { name: "swipe", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, mode: "swipe", from: "sk4rq2f" },
  { name: "shared", turns: 200, images: 3, imgdelay: 3000, mermaid: 0, mode: "shared", shared: true },
  { name: "typing", turns: 60, images: 0, imgdelay: 0, mermaid: 0, mode: "typing" },
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

// scrollTop/scrollHeight/clientHeight of the transcript, plus whether the "jump to latest" pill
// (「最新へ」) is showing. `scroller` is an expression returning the surface's scroll container
// (a different element for the mirror and for the shared view).
const probeIn = (scroller) => `(() => {
  const el = ${scroller};
  if (!el) return null;
  return {
    top: Math.round(el.scrollTop),
    gap: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
    turns: el.querySelectorAll("[data-turn-idx]").length,
    // "jump to latest" and "start of the reply" are the same pill (.mirror-jump), so exclude the
    // latter when counting.
    jump: !!document.querySelector(".mirror-jump:not(.mirror-jump-top)"),
    replyTop: !!document.querySelector(".mirror-jump-top"),
    // Which session's transcript is mounted - the stub offsets idx by +1000 for all but sk4rq2f.
    first: Number(el.querySelector("[data-turn-idx]")?.dataset.turnIdx ?? -1),
    // The topmost visible turn and its offset (sampled the same way as scrollMark). Restoration is
    // judged by which content is at the top edge, not by px: the height of the hundred-odd turns
    // above drifts a few px per visit (highlighting, fonts), so demanding an exact scrollTop would
    // fail even a healthy restore.
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
const PROBE = probeIn(`document.querySelector(".mirror-body")`);
const PROBE_SHARED = probeIn(`document.querySelector(".shared-view-body")`);
const wheelIn = (scroller) => `(() => {
  const r = ${scroller}.getBoundingClientRect();
  return JSON.stringify({ x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) });
})()`;
const WHEEL_UP = wheelIn(`document.querySelector(".mirror-body")`);
const WHEEL_UP_SHARED = wheelIn(`document.querySelector(".shared-view-body")`);
// The shared-session row in the left pane (receiving side). Only one is ever seeded, so the first
// one is enough.
const OPEN_SHARED = `(() => {
  const b = document.querySelector(".shared-rail-row");
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;
// The reader, parked at the bottom, expands the last work-in-progress (「作業過程」) disclosure. The content grows
// under them and they must STAY — snapping to the bottom would hide what they just opened
// (the regression 7f871de fixed, and the reason the re-pin used to be time-boxed).
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
const SESSION_A = "チェックアウトの入力検証"; // sk4rq2f - idx left as is (stub)
const SESSION_B = "返金 API の契約整理"; //     sc9lm3d - idx offset by +1000 (stub)
const OPEN_SESSION = openRow(SESSION_A);
// Where the horizontal swipe starts. Pick a turn's heading row: it is inside the transcript (where
// the finger's pointerdown arms the "the reader expanded this" window) and is not an element with
// its own horizontal scroll (a code block), so swipeGuard does not reject it. The left third
// belongs to the drawer, so take a point towards the right.
const SWIPE_FROM = `(() => {
  const body = document.querySelector(".mirror-body");
  if (!body) return "none";
  const br = body.getBoundingClientRect();
  return JSON.stringify({ x: Math.round(br.x + br.width * 0.7), y: Math.round(br.y + br.height / 2) });
})()`;

const pane = (session) => ({ id: "p0", session, content: { kind: "terminal", chat: true }, wrap: null });
const layout = (session) => ({ cols: [{ id: "c0", rowRatio: 0.5, panes: [pane(session)] }], colRatios: [1], activeId: "p0" });

// --- The behaviour behind each mode -------------------------------------------------

// land (default): opening lands at the bottom. Reflow the reader caused is not followed. A wheel
// breaks the follow.
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

  // …and the other half of the contract: a real wheel-up must STOP the follow, show the jump pill,
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
  // While pinned to the bottom the "start of the reply" pill stays hidden: that is where the
  // handoff card's launch button and the question card's answer buttons sit, and a floating pill
  // over them makes them unpressable (reported from real use).
  const ok = !!landed && landed.gap <= 2 && !landed.jump && !landed.replyTop && keptPlace && up.gap > 2 && up.jump && stayedPut;
  return {
    ok,
    note: `landed gap=${landed?.gap}px (turns=${landed?.turns}) replyTop=${landed?.replyTop}  expand-kept-place=${keptPlace}  wheel-up: gap=${up.gap}px jump=${up.jump} stayedPut=${stayedPut}`,
  };
}

// restore: read A partway -> switch to B -> back to A lands at the same place (scrollMark). It
// also checks that B lands at B's own bottom, i.e. the mark does not leak across sessions.
async function runRestore(cdp) {
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not find the session row in the left pane");
  await sleep(9000);
  const { x, y } = JSON.parse(await cdp.ev(WHEEL_UP));
  for (let i = 0; i < 4; i++) {
    await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: -400, pointerType: "mouse" });
    await sleep(80);
  }
  await sleep(1500);
  const before = await cdp.ev(PROBE); // the "read partway" position

  if ((await cdp.ev(openRow(SESSION_B))) !== "ok") throw new Error("could not find the second session row");
  await sleep(8000);
  const other = await cdp.ev(PROBE); // the other session lands at its own bottom

  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not re-open the first session");
  await sleep(9000);
  const back = await cdp.ev(PROBE);
  await sleep(2500);
  const settled = await cdp.ev(PROBE); // does it stay put once late layout has settled

  const sameSession = back.first === before.first && other.first !== before.first;
  // The same turn at the same offset from the top edge means it was restored (no px match
  // required - see PROBE above).
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

// shared: a shared session (receiving side). First open lands at the bottom; read partway, move to
// another session, come back and the position is the same. The same two points as on the owner's
// side, but landing and restoring belong to SharedSessionView itself - the transcript's height is
// only settled late, so without the ResizeObserver re-pin it never reaches the bottom (measured
// gap 2096px).
async function runShared(cdp) {
  if ((await cdp.ev(OPEN_SHARED)) !== "ok") throw new Error("could not find the shared session row");
  await sleep(9000);
  const landed = await cdp.ev(PROBE_SHARED);

  const { x, y } = JSON.parse(await cdp.ev(WHEEL_UP_SHARED));
  for (let i = 0; i < 4; i++) {
    await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: -400, pointerType: "mouse" });
    await sleep(80);
  }
  await sleep(1500);
  const before = await cdp.ev(PROBE_SHARED); // the "read partway" position

  // Move to one of our own sessions (the mirror) and back, so the shared view is unmounted and
  // remounted.
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not switch to an own session");
  await sleep(8000);
  if ((await cdp.ev(OPEN_SHARED)) !== "ok") throw new Error("could not re-open the shared session");
  await sleep(9000);
  const back = await cdp.ev(PROBE_SHARED);
  await sleep(2500);
  const settled = await cdp.ev(PROBE_SHARED); // does it stay put once late layout has settled

  const restored =
    !!before.anchor && !!back.anchor &&
    back.anchor.idx === before.anchor.idx &&
    Math.abs(back.anchor.off - before.anchor.off) <= 4;
  const stayed = Math.abs(settled.top - back.top) <= 5;
  const ok = !!landed && landed.gap <= 2 && !landed.jump && before.top > 0 && restored && stayed && back.jump;
  return {
    ok,
    note: `landed gap=${landed?.gap}px (turns=${landed?.turns})  left on turn ${before.anchor?.idx}@${before.anchor?.off}px → came back to ${back.anchor?.idx}@${back.anchor?.off}px (stayed=${stayed}, jump=${back.jump})`,
  };
}

// swipe: switch sessions with a horizontal phone swipe. The point is that the finger comes down on
// the transcript: when that pointerdown is carried over, the re-pin stops and the landing position
// becomes nondeterministic.
async function runSwipe(cdp) {
  await sleep(6000); // until the initial session (sc.from) has mounted and settled
  const before = await cdp.ev(PROBE);
  const at = await cdp.ev(SWIPE_FROM);
  if (at === "none") throw new Error("no turn head to start the swipe from");
  const { x, y } = JSON.parse(at);
  // Move left. Keep y fixed so it never falls into the vertical-first bail-out (|dx| <= |dy|).
  //
  // The steps are large and fast on purpose: Chromium coalesces consecutive touchMoves and reports
  // intermediate coordinates, so finer steps shrink the dx of a single event. If ROTATE_DIST=70 is
  // not crossed within LONG_PRESS_MS=500ms, the gesture is treated as a finger resting while the
  // user thinks and the candidate is cancelled (measured: with 120ms steps, 70px was only crossed
  // at 600ms and the swipe never took).
  await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y }] });
  for (const dx of [-120, -240]) {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: x + dx, y }] });
    await sleep(20);
  }
  await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });

  await sleep(9000); // the switched-to transcript plus its late layout
  const landed = await cdp.ev(PROBE);
  await sleep(2500);
  const settled = await cdp.ev(PROBE);

  // …and a small scroll up brings back the "start of the reply" pill, confirming it only retracts
  // at the bottom rather than having disappeared. Scrolling less than one reply's height matters:
  // scroll further and we end up above the latest reply itself, putting its start below the fold.
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

// typing: write a long draft in the composer while pinned to the bottom. The input grows with its
// content (up to .mirror-input's max-height), but shrinking it to two rows for a moment to measure
// that height grows the transcript's clientHeight by the same amount, and the browser clamps the
// scrollTop of a view sitting at the bottom. Restoring the height does not restore scrollTop, so
// the view floats off the bottom on every keystroke (measured 154px, larger the taller the input).
//
// Disable overflow-anchor on .mirror-body before measuring. Chromium's scroll anchoring cancels
// this clamping out, so a plain headless run is 3/3 green and the defect is invisible - the check
// would protect nothing. Anchoring is not a guaranteed behaviour: engines that lack it or have it
// suppressed show the defect straight through, and that is the side the user reports come from.
// Running with it off is the test of whether we are being rescued by anchoring. Measured: before
// the fix, the first keystroke gave gap=154px and even brought up the jump pill.
const KILL_ANCHOR = `(() => {
  const st = document.createElement("style");
  st.textContent = ".mirror-body { overflow-anchor: none; }";
  document.head.appendChild(st);
  return "ok";
})()`;
const COMPOSER_H = `(() => {
  const t = document.querySelector(".mirror-input");
  return t ? Math.round(t.getBoundingClientRect().height) : -1;
})()`;
async function typeChar(cdp, ch) {
  const common = { key: ch, text: ch, unmodifiedText: ch, windowsVirtualKeyCode: ch.toUpperCase().charCodeAt(0) };
  await cdp.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
  await cdp.send("Input.dispatchKeyEvent", { type: "char", ...common });
  await cdp.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
}
async function runTyping(cdp) {
  if ((await cdp.ev(OPEN_SESSION)) !== "ok") throw new Error("could not find the session row in the left pane");
  await sleep(9000);
  await cdp.ev(KILL_ANCHOR);
  const landed = await cdp.ev(PROBE);

  // Fill in a draft to grow the input (with newlines, so it really grows vertically). insertText is
  // a single input event, so this only exercises the moment of growth.
  if ((await cdp.ev(`(() => { const t = document.querySelector(".mirror-input"); if (!t) return "none"; t.focus(); return "ok"; })()`)) !== "ok")
    throw new Error("no composer input (session not live?)");
  await cdp.send("Input.insertText", { text: Array.from({ length: 10 }, (_, i) => `下書きの ${i} 行目です。`).join("\n") });
  await sleep(1200);
  const grown = await cdp.ev(PROBE);
  const h = await cdp.ev(COMPOSER_H);

  // Now the real point: add one character at a time to the fully grown input. The transcript must
  // stay at the bottom and not move.
  let worst = grown.gap;
  const tops = [];
  for (const ch of "abcde") {
    await typeChar(cdp, ch);
    await sleep(400);
    const p = await cdp.ev(PROBE);
    worst = Math.max(worst, p.gap);
    tops.push(p.top);
  }
  // Shrinking (deleting characters) goes through the same measurement.
  for (let i = 0; i < 5; i++) {
    await cdp.send("Input.dispatchKeyEvent", { type: "rawKeyDown", key: "Backspace", windowsVirtualKeyCode: 8, nativeVirtualKeyCode: 8 });
    await cdp.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Backspace", windowsVirtualKeyCode: 8, nativeVirtualKeyCode: 8 });
    await sleep(400);
  }
  const after = await cdp.ev(PROBE);
  worst = Math.max(worst, after.gap);

  const ok = !!landed && landed.gap <= 2 && h > 100 && worst <= 2 && !after.jump;
  return {
    ok,
    note: `landed gap=${landed?.gap}px  composer=${h}px  worst gap while typing=${worst}px (tops ${tops.join(",")})  after backspaces gap=${after.gap}px jump=${after.jump}`,
  };
}

async function runScenario(sc, chrome) {
  const stub = spawn(process.execPath, [path.join(HERE, "stub.mjs"), "--port", String(PORT),
    "--turns", String(sc.turns), "--images", String(sc.images), "--imgdelay", String(sc.imgdelay),
    "--mermaid", String(sc.mermaid), "--shared", sc.shared ? "1" : "0"], { stdio: ["ignore", "ignore", "inherit"] });
  try {
    await fetchJSON(`${BASE}api/whoami`);
    const results = [];
    for (let run = 0; run < RUNS; run++) {
      const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
      const cdp = await CDP.connect(target.webSocketDebuggerUrl);
      await cdp.send("Page.enable");
      // Only the swipe behaviour runs phone-sized (<=760px, the width at which the left pane
      // becomes an off-canvas drawer) with touch enabled.
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

      const r =
        sc.mode === "restore" ? await runRestore(cdp)
        : sc.mode === "swipe" ? await runSwipe(cdp)
        : sc.mode === "shared" ? await runShared(cdp)
        : sc.mode === "typing" ? await runTyping(cdp)
        : await runLanding(cdp);
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
