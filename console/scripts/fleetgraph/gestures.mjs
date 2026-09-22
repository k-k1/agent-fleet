// The fleet graph's touch gestures, driven as REAL touches and repeated (ADR 0096
// decision 16, docs/log/101 §101.14).
//
//   npm --prefix console run build
//   node console/scripts/fleetgraph/gestures.mjs [--cycles 6] [--lanes 78]
//
// Why a third harness: check.mjs proves a pinch and a drag each work ONCE, from a clean
// state. The defect this one exists for only appears after they are repeated — a finger's
// bookkeeping entry leaks, and from then on every one-finger drag is mistaken for half a
// pinch and the figure stops scrolling altogether. A user hit it on a phone within a
// minute of pinching and scrolling; no single-gesture check could have caught it.
//
// The ingredient that makes it leak is in here on purpose: the fingers come down ON the
// activity bands, whose React keys contain their clipped start/end times. A zoom changes
// the window, so those keys change, so the elements under the fingers are REMOVED
// mid-gesture — and a touch pointer's implicit capture points at exactly that removed
// element, so its pointerup is delivered to a node no longer in the document and never
// reaches the view.
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checker, sleep, startBrowser, until } from "../lib/headless.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const LOCALE = arg("locale", "ja");
const LANES = Number(arg("lanes", "78"));
const CYCLES = Number(arg("cycles", "6"));
const PORT = Number(arg("port", 8795));
const BASE = `http://127.0.0.1:${PORT}/`;
// A phone-shaped viewport: this is where the user met it, and the lanes must overflow.
const [W, H] = arg("size", "430x860").split("x").map(Number);

const layout = {
  cols: [{ id: "c0", rowRatio: 1, panes: [{ id: "p0", session: null, content: { kind: "fleetgraph", showArchived: true }, wrap: null }] }],
  colRatios: [1],
  activeId: "p0",
};

const server = spawn(
  process.execPath,
  [path.join(ROOT, "console/scripts/shots/server.mjs"), "--port", String(PORT), "--locale", LOCALE, "--fleet-lanes", String(LANES)],
  { stdio: ["ignore", "ignore", "inherit"] },
);
const cdp = await startBrowser({ size: `${W},${H}` });
const { check, report } = checker();
let code = 1;
try {
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try {
      localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE, theme: "dark" }))});
      localStorage.setItem("af-tenant", "demo");
      localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout))});
    } catch {}`,
  });
  await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
  await cdp.goto(BASE, 20000);
  const lanes = await until(cdp.evaluate, `document.querySelectorAll(".fgraph-label").length`, (n) => n > 10, 150, 200);
  check(lanes > 10, "the figure drew a fleet-sized number of lanes", `${lanes} lanes`);

  // The VISIBLE box, not the canvas's own: `.fgraph-canvas` is as tall as its content
  // (2,708px here), so a point taken from its rect lands far below the window and the
  // touch reaches nothing at all. Measured the first time this harness ran — every check
  // was red including the one before any pinch, which is what gave it away.
  const box = await cdp.evaluate(`(() => {
    const body = document.querySelector(".fgraph-body");
    const b = body.getBoundingClientRect();
    const labels = document.querySelector(".fgraph-labels").getBoundingClientRect();
    return { x: b.x, y: b.y, w: b.width, h: b.height, labelW: labels.width, scrollH: body.scrollHeight, clientH: body.clientHeight };
  })()`);
  check(box.scrollH > box.clientH + 1, "the lane list overflows (so a scroll can be observed)", `${box.scrollH} > ${box.clientH}`);

  // Aim at the activity bands, to the right of the label column: their keys carry the
  // clipped times, so a zoom removes the very elements the fingers are resting on. That
  // is the ingredient that makes a touch pointer's implicit capture go stale.
  const cx = Math.round(box.x + box.labelW + (box.w - box.labelW) * 0.5);
  const cy = Math.round(box.y + box.h * 0.35);
  console.log(`  ..   touching at (${cx}, ${cy}); visible box ${Math.round(box.w)}x${Math.round(box.h)}, labels ${Math.round(box.labelW)}px`);
  const scrollTop = () => cdp.evaluate(`document.querySelector(".fgraph-body").scrollTop`);
  const setScroll = (v) => cdp.evaluate(`document.querySelector(".fgraph-body").scrollTop = ${v}`);

  const pinch = async (spread) => {
    const gap = 40;
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x: cx - gap, y: cy, id: 1 }, { x: cx + gap, y: cy, id: 2 }],
    });
    for (let i = 1; i <= 5; i++) {
      const d = gap + spread * i * 12;
      await cdp.send("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [{ x: cx - d, y: cy, id: 1 }, { x: cx + d, y: cy, id: 2 }],
      });
      await sleep(16);
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(80);
  };

  const dragUp = async (px) => {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: cx, y: cy + px, id: 7 }] });
    for (let i = 1; i <= 6; i++) {
      await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: cx, y: cy + px - (px * i) / 6, id: 7 }] });
      await sleep(16);
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(120);
  };

  // 🔥 Does a pinch inside the figure zoom the PAGE? The viewport meta has no
  // `user-scalable=no` (index.html) and app/viewport.ts is written around the fact that a
  // page pinch-zoom happens — so any pinch the figure does not claim becomes one. Once the
  // page is zoomed, a finger drag pans the VISUAL VIEWPORT instead of the lane list, and
  // the figure reads as "scrolling stopped working" until the user pinches back out.
  const scale = () => cdp.evaluate(`window.visualViewport ? window.visualViewport.scale : 1`);
  const straddle = async () => {
    // One finger on the LABEL COLUMN, one on the canvas — the ordinary way a hand lands on
    // a 430px phone where the labels are 168px of it.
    const lx = Math.round(box.x + box.labelW * 0.5);
    await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: lx, y: cy, id: 31 }, { x: cx, y: cy, id: 32 }] });
    for (let i = 1; i <= 6; i++) {
      await cdp.send("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [{ x: Math.max(2, lx - i * 10), y: cy, id: 31 }, { x: cx + i * 14, y: cy, id: 32 }],
      });
      await sleep(16);
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(200);
  };
  const scaleBefore = await scale();
  await pinch(1);
  const scaleAfterCanvas = await scale();
  check(scaleAfterCanvas === scaleBefore, "a pinch ON THE CANVAS does not zoom the page", `scale ${scaleBefore} -> ${scaleAfterCanvas}`);
  await straddle();
  const scaleAfterStraddle = await scale();
  check(
    scaleAfterStraddle === scaleBefore,
    "a pinch straddling the label column does not zoom the page either",
    `scale ${scaleBefore} -> ${scaleAfterStraddle}`,
  );

  // A scroll works before any pinch — otherwise the rest proves nothing.
  await setScroll(0);
  await dragUp(120);
  const first = await scrollTop();
  check(first > 0, "a one-finger drag scrolls the lane list before any pinch", `scrollTop=${first}`);

  // Now the user's sequence: pinch, scroll, pinch, scroll…
  let deadAt = 0;
  for (let c = 1; c <= CYCLES; c++) {
    await pinch(c % 2 === 0 ? -1 : 1);
    await setScroll(0);
    await dragUp(120);
    const t = await scrollTop();
    if (t <= 0 && !deadAt) deadAt = c;
  }
  const after = await scrollTop();
  check(
    after > 0,
    `the lane list still scrolls after ${CYCLES} pinch/scroll cycles`,
    deadAt ? `dead from cycle ${deadAt}; scrollTop=${after}` : `scrollTop=${after}`,
  );

  // The bookkeeping itself, not just its symptom: nothing may be left in the book once
  // every finger is up. A leaked entry is what makes the next drag read as half a pinch.
  const stuck = await cdp.evaluate(`(() => {
    // A drag that does nothing is indistinguishable from a drag onto a lane that cannot
    // scroll, so ask the DOM a second way: a fresh one-finger drag must change scrollTop.
    const body = document.querySelector(".fgraph-body");
    return { top: body.scrollTop, max: body.scrollHeight - body.clientHeight };
  })()`);
  check(stuck.max > 0, "there is still somewhere to scroll to", `max=${stuck.max}`);

  // 🔥 The state the report is about, made directly: a finger whose pointerup never
  // arrived. On a phone that happens because a touch pointer is implicitly captured to the
  // element it went down on, and a zoom re-renders the activity bands out of the document
  // (their React keys carry the clipped times) — the up is then delivered to a node that
  // is no longer there. CDP's synthesized touches do not reproduce that retargeting, so
  // the state is built here instead of hoped for. One leaked entry is enough: every later
  // one-finger drag then has two "fingers" in the book and is read as half a pinch.
  await setScroll(0);
  await cdp.evaluate(`(() => {
    const el = document.querySelector(".fgraph-canvas");
    const r = el.getBoundingClientRect();
    el.dispatchEvent(new PointerEvent("pointerdown", {
      bubbles: true, pointerId: 9901, pointerType: "touch", isPrimary: true,
      clientX: r.x + 30, clientY: r.y + 30, buttons: 1,
    }));
  })()`);
  await sleep(60);
  await dragUp(120);
  const afterGhost = await scrollTop();
  check(afterGhost > 0, "a finger whose pointerup was lost does not kill scrolling", `scrollTop=${afterGhost}`);

  // An interrupted gesture must not poison the next one either: the browser cancels a
  // touch whenever it takes a gesture over, and that path has to clear the book too.
  await setScroll(0);
  await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: cx, y: cy, id: 11 }, { x: cx + 60, y: cy, id: 12 }] });
  await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: cx - 20, y: cy, id: 11 }, { x: cx + 90, y: cy, id: 12 }] });
  await cdp.send("Input.dispatchTouchEvent", { type: "touchCancel", touchPoints: [] });
  await sleep(120);
  await dragUp(120);
  const afterCancel = await scrollTop();
  check(afterCancel > 0, "a cancelled pinch does not leave the figure unscrollable", `scrollTop=${afterCancel}`);

  // And a pinch whose fingers lift one at a time (the ordinary way a hand leaves the
  // glass) must end just as cleanly as one that lifts both at once.
  await setScroll(0);
  await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: cx - 40, y: cy, id: 21 }, { x: cx + 40, y: cy, id: 22 }] });
  await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: cx - 80, y: cy, id: 21 }, { x: cx + 80, y: cy, id: 22 }] });
  await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [{ x: cx + 80, y: cy, id: 22 }] });
  await sleep(40);
  await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await sleep(120);
  await dragUp(120);
  const afterStaggered = await scrollTop();
  check(afterStaggered > 0, "fingers lifting one at a time leave the figure scrollable", `scrollTop=${afterStaggered}`);

  await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: false });
  for (const l of cdp.logs) console.log("[page]", l);
  check(cdp.logs.length === 0, "the page threw nothing", cdp.logs.join(" / "));
  code = report();
} finally {
  cdp.close();
  server.kill();
}
process.exit(code);
