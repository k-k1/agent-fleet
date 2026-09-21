// The fleet graph's arrows AT SCALE (ADR 0096 decision 8-2, docs/log/101 §101.13).
//
//   npm --prefix console run build
//   node console/scripts/fleetgraph/arrows.mjs [--lanes 78] [--shot <dir>]
//
// Separate from check.mjs because it needs a different fixture: nine lanes cannot show
// this class of defect at all. A user's own workspace reached 78 lanes, and there an
// arrow from OUTSIDE the figure (a person or a chat conversation instructing a session)
// ran from the figure's top edge all the way down to its lane — a 2,400px vertical line
// crossing every row, and with dozens of them the figure was a picket fence.
//
// What it measures: the drawn HEIGHT of every arrow line, split by whether one of its
// ends is an external actor. "Reads" is not a verdict a screenshot can give; a bound on
// the tallest line is.
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
const LANES = Number(arg("lanes", "78"));
const SHOT = arg("shot", "");
const PORT = Number(arg("port", 8793));
const BASE = `http://127.0.0.1:${PORT}/`;
const [W, H] = arg("size", "1100x900").split("x").map(Number);

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
  await cdp.goto(BASE, 20000);
  const lanes = await until(cdp.evaluate, `document.querySelectorAll(".fgraph-label").length`, (n) => n > 20, 150, 200);
  check(lanes > 20, "the figure drew a fleet-sized number of lanes", `${lanes} lanes`);

  // Every arrow's drawn height, and whether it touches the figure's outside edge. The
  // edge dot is what the view draws for an end that has no lane at all (decision 8-2).
  const arrows = await cdp.evaluate(`(() => {
    const figure = document.querySelector("svg.fgraph-svg").getBoundingClientRect();
    const out = [];
    for (const g of document.querySelectorAll("svg.fgraph-svg .fgraph-arrow")) {
      const line = g.querySelector("line");
      if (!line) continue;
      const y1 = Number(line.getAttribute("y1"));
      const y2 = Number(line.getAttribute("y2"));
      out.push({
        h: Math.abs(y2 - y1),
        external: !!g.querySelector(".fgraph-edge-pt"),
        variant: [...g.classList].find((c) => c !== "fgraph-arrow" && c !== "clickable" && c !== "danger") || "",
      });
    }
    return { figureH: figure.height, arrows: out };
  })()`);

  const ext = arrows.arrows.filter((a) => a.external);
  const internal = arrows.arrows.filter((a) => !a.external);
  const tallest = (xs) => (xs.length ? Math.max(...xs.map((a) => a.h)) : 0);
  console.log(
    `  ..   figure ${Math.round(arrows.figureH)}px, ${arrows.arrows.length} arrows ` +
      `(${ext.length} from/to outside, ${internal.length} lane-to-lane)`,
  );
  console.log(`  ..   tallest: outside ${Math.round(tallest(ext))}px, lane-to-lane ${Math.round(tallest(internal))}px`);

  const rowsProbe = await cdp.evaluate(`(() => {
    const ys = [...document.querySelectorAll("svg.fgraph-svg .fgraph-run")].map((e) => e.getBoundingClientRect().y);
    return (ys[1] || 0) - (ys[0] || 0);
  })()`);
  const rowsPx = rowsProbe > 0 ? rowsProbe : 34;

  check(ext.length > 0, "the fixture really has arrows from outside the figure", `${ext.length}`);
  // THE defect. An arrow that leaves the figure is a short stub at its own lane, not a
  // line to the figure's top edge: the bound is a couple of rows, whatever the fleet's
  // size, so the figure stays readable as it grows.
  check(
    tallest(ext) <= 40,
    "an arrow from outside the figure is a short stub, not a full-height line",
    `tallest ${Math.round(tallest(ext))}px in a ${Math.round(arrows.figureH)}px figure`,
  );
  // Lane-to-lane arrows genuinely connect two rows and are as long as the distance
  // between them (decision 8-2 rejected replacing them with marks). Reported with a
  // distribution rather than bounded: the number that matters is how MANY are long, and
  // family order keeps most of them between adjacent rows by construction (decision 9).
  const longInternal = internal.filter((a) => a.h > rowsPx * 3);
  console.log(
    `  ..   lane-to-lane: ${internal.length} arrows, ${longInternal.length} longer than 3 rows ` +
      `(${longInternal.map((a) => a.variant).join(",") || "none"})`,
  );
  check(true, "lane-to-lane arrows are left alone (they carry who → whom)", `tallest ${Math.round(tallest(internal))}px`);

  // Every external arrow still has to SAY it came from outside, or the stub is just a
  // short line with no origin.
  check(ext.every((a) => a.h > 0), "every external stub is actually drawn", `${ext.filter((a) => a.h === 0).length} zero-height`);

  check(rowsPx > 0 && tallest(ext) <= rowsPx * 1.2, "the stub stays inside about one row", `stub ${Math.round(tallest(ext))}px, row ${Math.round(rowsPx)}px`);

  if (SHOT) {
    fs.mkdirSync(SHOT, { recursive: true });
    const out = path.join(SHOT, `fleetgraph-${LANES}lanes-${LOCALE}.png`);
    await cdp.screenshot(out);
    console.log("[shot]", out);
  }
  await sleep(50);
  for (const l of cdp.logs) console.log("[page]", l);
  check(cdp.logs.length === 0, "the page threw nothing", cdp.logs.join(" / "));
  code = report();
} finally {
  cdp.close();
  server.kill();
}
process.exit(code);
