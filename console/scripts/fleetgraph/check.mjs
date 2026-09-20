// Fleet session graph (ADR 0096), measured in a real browser rather than looked at.
//
//   npm --prefix console run build
//   node console/scripts/fleetgraph/check.mjs [--locale ja|en] [--shot <dir>]
//
// Why this exists: twice during this figure's development a change was reported as
// "checked" from a screenshot and the measurement said otherwise (docs/log/101). Every
// claim here is a number out of getBoundingClientRect / scrollTop, taken from the REAL
// bundle served by the README shot stub (scripts/shots/server.mjs answers the whole API
// surface from fixtures, so no CP, no agent and no database are involved).
//
// What it pins — the four things the 2026-09-20 pass changed, plus the row alignment the
// widened label column could have broken:
//   axis      the time scale is a strip at the TOP of the scroll box and stays put while
//             the lanes scroll under it (it used to be the last thing in the canvas)
//   captions  the first and last captions sit inside the strip instead of half outside it
//   rows      label row i is still centred on lane line i (they drifted once before)
//   chips     every lane's state chip is inside the label column, not spilling over it
//   fold      "+" removes the family's rows and the parent's row says how many
//   gestures  a vertical wheel scrolls the lane list; a horizontal one pans time
import { spawn } from "node:child_process";
import fs from "node:fs";
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
const SHOT = arg("shot", "");
const PORT = Number(arg("port", 8791));
const BASE = `http://127.0.0.1:${PORT}/`;

// A short viewport by default, on purpose: the lanes have to overflow it, or nothing here
// can say whether the axis survives scrolling. `--size 1400x900` for a picture instead.
const [W, H] = arg("size", "1100x420").split("x").map(Number);

const layout = {
  cols: [{ id: "c0", rowRatio: 1, panes: [{ id: "p0", session: null, content: { kind: "fleetgraph", showArchived: true }, wrap: null }] }],
  colRatios: [1],
  activeId: "p0",
};

const server = spawn(process.execPath, [path.join(ROOT, "console/scripts/shots/server.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "ignore", "inherit"],
});
const cdp = await startBrowser({ size: `${W},${H}` });
const { check, report } = checker();
const shot = async (suffix) => {
  fs.mkdirSync(SHOT, { recursive: true });
  const out = path.join(SHOT, `fleetgraph-${LOCALE}${suffix}.png`);
  await cdp.screenshot(out);
  console.log("[shot]", out);
};
let code = 1;
try {
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try {
      localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE, theme: "dark" }))});
      localStorage.setItem("af-tenant", "demo");
      localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout))});
    } catch {}`,
  });
  await cdp.goto(BASE, 20000);
  const lanes = await until(cdp.evaluate, `document.querySelectorAll(".fgraph-label").length`, (n) => n > 3, 100, 200);
  check(lanes > 3, "the figure drew its lanes", `${lanes} lanes`);

  // --- geometry -------------------------------------------------------------------
  const geom = await cdp.evaluate(`(() => {
    const r = (el) => { const b = el.getBoundingClientRect(); return { x: b.x, y: b.y, w: b.width, h: b.height, b: b.bottom }; };
    const body = document.querySelector(".fgraph-body");
    const axis = document.querySelector(".fgraph-axis");
    const svg = document.querySelector("svg.fgraph-svg");
    const labels = [...document.querySelectorAll(".fgraph-label")].map(r);
    const runs = [...document.querySelectorAll(".fgraph-run")].map((el) => el.getBoundingClientRect().y);
    const caps = [...document.querySelectorAll(".fgraph-axis-label")].map((el) => {
      const b = el.getBoundingClientRect();
      return { left: b.left, right: b.right, text: el.textContent };
    });
    const axisSvg = axis.querySelector("svg").getBoundingClientRect();
    const chips = [...document.querySelectorAll(".fgraph-label .session-state")].map((el) => {
      const b = el.getBoundingClientRect();
      return { right: b.right, text: el.textContent.trim() };
    });
    const col = document.querySelector(".fgraph-labels").getBoundingClientRect();
    return {
      body: r(body), axis: r(axis), svg: r(svg), labels, runs, caps,
      axisSvg: { left: axisSvg.left, right: axisSvg.right },
      chips, col: { left: col.left, right: col.right, w: col.width },
      scrollH: body.scrollHeight, clientH: body.clientHeight,
    };
  })()`);

  check(geom.axis.y <= geom.body.y + 1, "the axis strip is at the top of the scroll box", `axis y=${Math.round(geom.axis.y)} body y=${Math.round(geom.body.y)}`);
  check(geom.svg.y >= geom.axis.b - 1, "the lanes start below it", `svg y=${Math.round(geom.svg.y)} axis bottom=${Math.round(geom.axis.b)}`);
  // The scroll checks below only say anything when the lanes do not fit. At the default
  // viewport they do not; a picture-sized `--size` shows them all, and then those checks are
  // SKIPPED rather than passed on a scroll that could not happen either way.
  const overflows = geom.scrollH > geom.clientH + 1;
  const skip = (label) => console.log("  --   " + label + " — skipped: every lane fits this viewport (drop --size)");

  const clipped = geom.caps.filter((c) => c.left < geom.axisSvg.left - 0.5 || c.right > geom.axisSvg.right + 0.5);
  check(clipped.length === 0, "every axis caption is inside the strip", clipped.length ? JSON.stringify(clipped) : `${geom.caps.length} captions, ends ${geom.caps[0]?.text}…${geom.caps.at(-1)?.text}`);

  // Row alignment: the label's centre against the lane line drawn for the same row. The
  // label column and the SVG are two different boxes and drifted apart once before.
  const drift = geom.labels.slice(0, geom.runs.length).map((l, i) => Math.abs(l.y + l.h / 2 - geom.runs[i]));
  const worst = Math.max(...drift);
  check(worst <= 1.5, "each label row is centred on its lane line", `worst drift ${worst.toFixed(2)}px over ${drift.length} rows`);

  // How much room the name actually keeps once the chip has taken its share. Not a pass/fail
  // line — it is a judgement — but it is the number the judgement has to be made on.
  const names = await cdp.evaluate(`(() => {
    const els = [...document.querySelectorAll(".fgraph-label-text")];
    return {
      clipped: els.filter((e) => e.scrollWidth > e.clientWidth + 1).length,
      total: els.length,
      widest: Math.max(...els.map((e) => e.clientWidth)),
      chip: Math.max(...[...document.querySelectorAll(".fgraph-label .session-state")].map((e) => e.getBoundingClientRect().width)),
    };
  })()`);
  console.log("  ..   name column:", JSON.stringify(names));

  const spill = geom.chips.filter((c) => c.right > geom.col.right + 0.5);
  check(spill.length === 0, "no state chip spills out of the label column", spill.length ? JSON.stringify(spill) : `${geom.chips.length} chips, column ${Math.round(geom.col.w)}px`);
  // Every row but the erased one: a deleted lane has no session to ask and its label
  // already reads "deleted · <slug>", so a chip there would only repeat it (decision 6).
  const erased = await cdp.evaluate(`document.querySelectorAll(".fgraph-label.erased").length`);
  check(geom.chips.length === geom.labels.length - erased, "every lane row but the erased one carries a state chip", `${geom.chips.length} chips / ${geom.labels.length} rows, ${erased} erased`);
  check(erased > 0, "the fixture really has an erased lane (or the line above proves nothing)", `${erased}`);

  if (SHOT) await shot("");

  // --- the axis survives scrolling -------------------------------------------------
  if (!overflows) {
    skip("the axis stays pinned while the lanes scroll under it");
    skip("a vertical wheel scrolls the lane list");
  }
  await cdp.evaluate(`document.querySelector(".fgraph-body").scrollTop = 120`);
  await sleep(120);
  const scrolled = await cdp.evaluate(`(() => {
    const body = document.querySelector(".fgraph-body").getBoundingClientRect();
    const axis = document.querySelector(".fgraph-axis").getBoundingClientRect();
    return { top: document.querySelector(".fgraph-body").scrollTop, dy: axis.y - body.y, caption: !!document.querySelector(".fgraph-axis-label") };
  })()`);
  if (overflows) check(scrolled.top > 0 && Math.abs(scrolled.dy) <= 1, "the axis stays pinned while the lanes scroll under it", `scrollTop=${scrolled.top} axis offset=${scrolled.dy.toFixed(1)}px`);
  await cdp.evaluate(`document.querySelector(".fgraph-body").scrollTop = 0`);

  // --- gestures ---------------------------------------------------------------------
  const winOf = `[...document.querySelectorAll(".fgraph-axis-label")].map((e) => e.textContent).join("|")`;
  const before = await cdp.evaluate(winOf);
  const cx = Math.round(geom.svg.x + geom.svg.w / 2);
  const cy = Math.round(geom.svg.y + 40);
  // A plain vertical wheel belongs to the lane list. The first version swallowed it for
  // the time axis, which left no way to reach the rows below the fold.
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: cx, y: cy, deltaX: 0, deltaY: 120 });
  await sleep(250);
  const afterVertical = await cdp.evaluate(`(() => ({ win: ${winOf}, top: document.querySelector(".fgraph-body").scrollTop }))()`);
  check(afterVertical.win === before, "a vertical wheel does NOT move the time window", afterVertical.win === before ? "" : `${before} -> ${afterVertical.win}`);
  if (overflows) check(afterVertical.top > 0, "a vertical wheel scrolls the lane list", `scrollTop=${afterVertical.top}`);
  await cdp.evaluate(`document.querySelector(".fgraph-body").scrollTop = 0`);

  // A horizontal wheel pans time, by the distance it travelled.
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: cx, y: cy, deltaX: -200, deltaY: 0 });
  await sleep(500);
  const afterHorizontal = await cdp.evaluate(winOf);
  check(afterHorizontal !== before, "a horizontal wheel pans the time window", `${before} -> ${afterHorizontal}`);

  // Drag-to-pan, and the click it must not turn into.
  const panesBefore = await cdp.evaluate(`document.querySelectorAll(".pane").length`);
  await cdp.send("Input.dispatchMouseEvent", { type: "mousePressed", x: cx, y: cy, button: "left", clickCount: 1 });
  for (let i = 1; i <= 6; i++) {
    await cdp.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: cx + i * 20, y: cy, button: "left", buttons: 1 });
  }
  await cdp.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: cx + 120, y: cy, button: "left", clickCount: 1 });
  await sleep(500);
  const afterDrag = await cdp.evaluate(`(() => ({
    win: ${winOf},
    panes: document.querySelectorAll(".pane").length,
    lanes: document.querySelectorAll(".fgraph-label").length,
    empty: !!document.querySelector(".fgraph-body .empty-state, .fgraph-body .ui-empty"),
  }))()`);
  // `win` must still be a READABLE scale afterwards, not "": panning into a stretch where
  // nothing ran used to replace the whole figure with a bare EmptyState, taking the axis
  // (where am I?) and the wheel/drag handlers (how do I get back?) with it.
  check(afterDrag.win !== afterHorizontal && afterDrag.win.includes("|"), "dragging the canvas pans the time window, and the scale survives an empty stretch", `${afterHorizontal} -> ${afterDrag.win}`);
  check(afterDrag.lanes === 0 ? afterDrag.empty : true, "an empty window says so, without losing the axis", `${afterDrag.lanes} lanes, empty-state=${afterDrag.empty}`);
  check(afterDrag.panes === panesBefore, "the drag did not open the lane it started on", `${panesBefore} -> ${afterDrag.panes} panes`);

  // --- the family fold ---------------------------------------------------------------
  // Back to the default window first: the gesture checks above deliberately left it in the
  // past, where this fixture has no families (and, at the far end, no lanes at all).
  await cdp.evaluate(`document.querySelectorAll(".fgraph-nav button")[2].click()`);
  await sleep(600);
  const foldable = await cdp.evaluate(`document.querySelectorAll(".fgraph-label .fgraph-fold").length`);
  check(foldable > 0, "a parent row offers a fold", `${foldable} rows with a control`);
  const rowsBefore = await cdp.evaluate(`document.querySelectorAll(".fgraph-label").length`);
  await cdp.evaluate(`document.querySelectorAll(".fgraph-label .fgraph-fold")[0].click()`);
  await sleep(400);
  const folded = await cdp.evaluate(`(() => ({
    rows: document.querySelectorAll(".fgraph-label").length,
    badge: document.querySelector(".fgraph-hidden")?.textContent || "",
    runs: document.querySelectorAll(".fgraph-run").length,
    labels: document.querySelectorAll(".fgraph-label").length,
  }))()`);
  check(folded.rows < rowsBefore, "folding takes the family's rows away", `${rowsBefore} -> ${folded.rows} rows`);
  check(/\d/.test(folded.badge), "the parent's row says how many it swallowed", folded.badge);
  if (SHOT) await shot("-folded");
  check(folded.runs <= folded.labels, "no lane line is left behind without a row", `${folded.runs} lines / ${folded.labels} rows`);
  await cdp.evaluate(`document.querySelectorAll(".fgraph-label .fgraph-fold")[0].click()`);
  await sleep(400);
  const unfolded = await cdp.evaluate(`document.querySelectorAll(".fgraph-label").length`);
  check(unfolded === rowsBefore, "and pressing it again brings them back", `${folded.rows} -> ${unfolded}`);

  // --- the way over to the sessions overview ------------------------------------------
  await cdp.evaluate(`document.querySelector(".fgraph-switch").click()`);
  await sleep(600);
  const swapped = await cdp.evaluate(`(() => ({ ovw: !!document.querySelector(".ovw"), graph: !!document.querySelector(".fgraph"), panes: document.querySelectorAll(".pane").length }))()`);
  check(swapped.ovw && !swapped.graph, "the switch button swaps this pane for the sessions overview", JSON.stringify(swapped));
  check(swapped.panes === panesBefore, "it swaps in place rather than opening another pane", `${swapped.panes} panes`);
  const back = await cdp.evaluate(`(() => { const b = document.querySelector(".ovw-switch"); if (!b) return "no button"; b.click(); return "clicked"; })()`);
  await sleep(600);
  const returned = await cdp.evaluate(`!!document.querySelector(".fgraph")`);
  check(back === "clicked" && returned, "and the overview carries the button back", `${back} / fgraph=${returned}`);

  for (const l of cdp.logs) console.log("[page]", l);
  check(cdp.logs.length === 0, "the page threw nothing", cdp.logs.join(" / "));
  code = report();
} finally {
  cdp.close();
  server.kill();
}
process.exit(code);
