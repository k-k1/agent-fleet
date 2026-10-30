// Settings section headings, measured: every .ds-title that has something above it inside the
// section sits at least MIN_GAP px below it, at desktop and at phone width (#2813).
//
//   npm --prefix console run build
//   node console/scripts/settings-headings/check.mjs [--locale en|ja] [--min 16] [--port N]
//
// Why a browser: the spacing comes from margin rules that only match some markup shapes
// (`* + .ds-title`, `.ds-group`, `* + :not(.ds-group) > .ds-title:first-child`), and a heading
// that opens a wrapper the rules did not foresee sat at 0 px with nothing in jsdom to notice.
// The numbers come from getBoundingClientRect on the REAL bundle, served by the README shot
// stub (scripts/shots/server.mjs), so no CP, agent or database is involved.
//
// The gap is taken to the nearest preceding visible block: the heading's previous sibling, or
// its wrapper's, climbing until .settings-content. A heading with nothing visible above it is
// the top of its section and must stay there (at most TOP_SLACK px below the section's start):
// a rule that matches a hidden sibling, such as the desktop-hidden breadcrumb, pushes it down.
//
// What must render is pinned, so a slow or broken fixture cannot pass by measuring less: the
// Skills headings (#2813's case) must all be measured at both sizes, and only the sections in
// NO_HEADINGS may render none.
import { spawn } from "node:child_process";
import net from "node:net";
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
// Only the locales with a catalog: any other value is stored as-is and the UI falls back to
// English, so the run would measure something other than what was asked.
if (!["en", "ja"].includes(LOCALE)) {
  console.error(`--locale must be en or ja, not ${LOCALE}`);
  process.exit(2);
}
const MIN_GAP = Number(arg("min", 16));
const TOP_SLACK = 4;
// Sections whose stub data renders no .ds-title at all. Anything else rendering none is a
// failure: it means the section broke, not that it has no headings.
const NO_HEADINGS = new Set([
  "account", "memory", "memorysnapshots", "tracker", "chat", "mcp", "tokens", "usage", "cost", "uptime", "internalrepos",
]);
const SKILLS = "skills";
const SKILLS_HEADINGS = 4;
// Sections are named by their key (the rail's data-section), never by label: labels change
// with the locale and with copy edits, and a stale one silently drops out of these lists.
// The stub has no fixture for the limit board, and that section takes the dialog down without one.
const SKIP = new Set(["limits"]);

const ACCOUNT_BTN = "[...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('@'))";

// A free port, asked of the OS rather than fixed: the other check scripts and other sessions'
// stubs use fixed ones, and answering from someone else's server would measure the wrong bundle.
const freePort = () =>
  new Promise((res, rej) => {
    const s = net.createServer();
    s.once("error", rej);
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => res(port));
    });
  });
const PORT = Number(arg("port", 0)) || (await freePort());
const BASE = `http://127.0.0.1:${PORT}/`;

async function openSettings(cdp) {
  await cdp.goto(BASE);
  // The locale is seeded before the first load (walk); make sure it took, or a run asked for
  // ja would measure the English UI and pass.
  const lang = await until(cdp.evaluate, "document.documentElement.lang", (v) => v === LOCALE, 100);
  if (lang !== LOCALE) throw new Error(`the Console came up in "${lang}", not "${LOCALE}"`);
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
  const content = document.querySelector(".settings-content");
  const crumb = content.querySelector(".settings-crumb");
  // On a phone the section starts under the back row and its own bottom margin.
  const start = crumb && vis(crumb)
    ? crumb.getBoundingClientRect().bottom + parseFloat(getComputedStyle(crumb).marginBottom)
    : content.getBoundingClientRect().top;
  return [...document.querySelectorAll(".settings-content .ds-title")].filter(vis).map((h) => {
    const p = above(h);
    const top = h.getBoundingClientRect().top;
    return [(h.textContent || "").trim().slice(0, 40), p ? Math.round(top - p.getBoundingClientRect().bottom) : null, Math.round(top - start)];
  });
})()`;

async function walk(size, check) {
  const cdp = await startBrowser({ size });
  try {
    // The UI language comes from the stored display settings, else the browser's own language;
    // seed it the way scripts/shots/capture.mjs does so --locale is what gets measured.
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `try { localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE }))}); } catch (e) {}`,
    });
    const n = await openSettings(cdp);
    let measured = 0;
    for (let i = 0; i < n; i++) {
      if (!(await cdp.evaluate("!!document.querySelector('.settings-rail-item')"))) await openSettings(cdp);
      const [key, name] = await cdp.evaluate(`(() => { const it = document.querySelectorAll('.settings-rail-item')[${i}]; return it ? [it.dataset.section || "", (it.textContent || "").trim()] : ["", ""]; })()`);
      if (!key || SKIP.has(key)) continue;
      await cdp.evaluate(`document.querySelectorAll('.settings-rail-item')[${i}].click()`);
      await sleep(1500);
      if (key === SKILLS) {
        // The catalog loads after the click; wait for every scope group rather than a fixed time.
        await until(cdp.evaluate, "document.querySelectorAll('.settings-content .ds-title').length", (v) => v >= SKILLS_HEADINGS, 100);
      }
      const rows = await cdp.evaluate(MEASURE);
      if (NO_HEADINGS.has(key)) {
        check(rows.length === 0, `${size} ${name}: renders no heading on the stub (allow-listed)`, `${rows.length} headings${rows.length ? " (update NO_HEADINGS)" : ""}`);
      } else {
        check(rows.length > 0, `${size} ${name}: renders its headings`, `${rows.length}`);
      }
      let gaps = 0;
      for (const [heading, gap, top] of rows) {
        if (gap === null) {
          check(top <= TOP_SLACK, `${size} ${name} › ${heading} stays at the top of the section`, `${top}px below its start`);
          continue;
        }
        measured++;
        gaps++;
        check(gap >= MIN_GAP, `${size} ${name} › ${heading}`, `${gap}px`);
      }
      if (key === SKILLS) check(gaps === SKILLS_HEADINGS, `${size} Skills: all ${SKILLS_HEADINGS} headings measured`, `${gaps}`);
      // Phone: the drill-down shows one section at a time; go back to the rail.
      await cdp.evaluate("document.querySelector('.settings-layout.entered .settings-back')?.click()");
      await sleep(300);
    }
    check(measured > 20, `${size}: measured a meaningful number of headings`, `${measured}`);
    for (const l of cdp.logs) console.log("[page]", l);
  } finally {
    cdp.close();
  }
}

const server = spawn(process.execPath, [path.join(ROOT, "console/scripts/shots/server.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "pipe", "inherit"],
});
// Wait for the stub's own "listening" line; a stub that dies first (EADDRINUSE, a broken
// fixture) fails the run instead of leaving it to measure whatever answers on the port.
await new Promise((res, rej) => {
  let out = "";
  const timer = setTimeout(() => rej(new Error("the stub did not start within 15 s")), 15000);
  server.stdout.on("data", (d) => {
    out += d;
    if (out.includes(`127.0.0.1:${PORT}`)) {
      clearTimeout(timer);
      res();
    }
  });
  server.once("exit", (c) => {
    clearTimeout(timer);
    rej(new Error(`the stub exited (${c}) before listening`));
  });
  server.once("error", (e) => {
    clearTimeout(timer);
    rej(e);
  });
}).catch((e) => {
  console.error(e.message);
  server.kill();
  process.exit(2);
});
server.stdout.resume();
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
