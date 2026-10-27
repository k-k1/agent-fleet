// Settings section headings, measured: every .ds-title that has something above it inside the
// section sits at least MIN_GAP px below it, at desktop and at phone width (#2813).
//
//   npm --prefix console run build
//   node console/scripts/settings-headings/check.mjs [--locale ja|en] [--min 16]
//
// Why a browser: the spacing comes from margin rules that only match some markup shapes
// (`* + .ds-title`, `.ds-group`, `* + :not(.ds-group) > .ds-title:first-child`), and a heading
// that opens a wrapper the rules did not foresee sat at 0 px with nothing in jsdom to notice.
// The numbers come from getBoundingClientRect on the REAL bundle, served by the README shot
// stub (scripts/shots/server.mjs), so no CP, agent or database is involved.
//
// The gap is taken to the nearest preceding visible block: the heading's previous sibling, or
// its wrapper's, climbing until .settings-content. A heading with nothing above it in the
// section (the first one) is not measured. Sections whose stub data renders no heading are
// listed, so a run that measured nothing cannot pass silently.
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
const LOCALE = arg("locale", "en");
const PORT = Number(arg("port", 8793));
const MIN_GAP = Number(arg("min", 16));
const BASE = `http://127.0.0.1:${PORT}/`;
// The stub has no fixture for these, and the section takes the whole dialog down without one.
const SKIP = /^(Limit board|利用枠ボード)$/;

const ACCOUNT_BTN = "[...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('@'))";

async function openSettings(cdp) {
  await cdp.goto(BASE);
  await until(cdp.evaluate, `!!${ACCOUNT_BTN}`, (v) => v, 200);
  await cdp.evaluate(`${ACCOUNT_BTN}.click()`);
  await sleep(600);
  await cdp.evaluate("[...document.querySelectorAll('[role=menuitem]')].find((m) => /設定|Settings/.test(m.textContent || '')).click()");
  return until(cdp.evaluate, "document.querySelectorAll('.settings-rail-item').length", (v) => v > 5, 100);
}

const MEASURE = `(() => {
  const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const above = (el) => {
    for (let n = el; n && !n.classList.contains("settings-content"); n = n.parentElement) {
      for (let p = n.previousElementSibling; p; p = p.previousElementSibling) if (vis(p) && !p.classList.contains("settings-crumb")) return p;
    }
    return null;
  };
  return [...document.querySelectorAll(".settings-content .ds-title")].filter(vis).map((h) => {
    const p = above(h);
    return [(h.textContent || "").trim().slice(0, 40), p ? Math.round(h.getBoundingClientRect().top - p.getBoundingClientRect().bottom) : null];
  });
})()`;

async function walk(size, check) {
  const cdp = await startBrowser({ size });
  try {
    const n = await openSettings(cdp);
    let measured = 0;
    const empty = [];
    for (let i = 0; i < n; i++) {
      if (!(await cdp.evaluate("!!document.querySelector('.settings-rail-item')"))) await openSettings(cdp);
      const name = await cdp.evaluate(`(document.querySelectorAll('.settings-rail-item')[${i}]?.textContent || '').trim()`);
      if (!name || SKIP.test(name)) continue;
      await cdp.evaluate(`document.querySelectorAll('.settings-rail-item')[${i}].click()`);
      await sleep(1500);
      const rows = await cdp.evaluate(MEASURE);
      if (!rows.length) empty.push(name);
      for (const [heading, gap] of rows) {
        if (gap === null) continue;
        measured++;
        check(gap >= MIN_GAP, `${size} ${name} › ${heading}`, `${gap}px`);
      }
      // Phone: the drill-down shows one section at a time; go back to the rail.
      await cdp.evaluate("document.querySelector('.settings-layout.entered .settings-back')?.click()");
      await sleep(300);
    }
    check(measured > 20, `${size}: measured a meaningful number of headings`, `${measured} measured; no headings in: ${empty.join(", ")}`);
    for (const l of cdp.logs) console.log("[page]", l);
  } finally {
    cdp.close();
  }
}

const server = spawn(process.execPath, [path.join(ROOT, "console/scripts/shots/server.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "ignore", "inherit"],
});
let code = 1;
try {
  const { check, report } = checker();
  await walk("1280,1000", check);
  await walk("390,844", check);
  code = report();
} finally {
  server.kill();
}
process.exit(code);
