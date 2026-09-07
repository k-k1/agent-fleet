// Does the Markdown a viewer pane renders follow Settings > Display > File viewer font size?
//
// The regression this guards (2026-09-07): `.markdown` carried `font: 14px/1.7 …` — a fixed
// size — while every other viewer surface read `--viewer-size` (codegrid.css, plain.css,
// diff.css). The setting, and the keyboard zoom that moves it for a viewer pane
// (lib/viewFont.ts), therefore did nothing whenever the pane showed Markdown: the var reached
// the element, the text did not move. Fenced code blocks had the same fault at 12px, and the
// sticky heading breadcrumb — a child of the scroller, not of .markdown — inherited body size.
//
// The same MarkdownView is rendered by three surfaces with three different sizes, so the
// check measures all of them together: the viewer follows --viewer-size, the mirror follows
// --chat-size (.mirror-turn .markdown) and the assistant chat inherits its bubble
// (.chatview .markdown, `font: inherit`). A fix that reaches the viewer by re-sizing the
// shared rule in em would move the other two, so "the others did NOT move" is as much of the
// contract as "the viewer did".
//
// This is a pure cascade fault: it needs a layout engine but no bundle, no CP and no agent.
// The page links the REAL stylesheets — never a hand-picked subset, since a sheet left out is
// reported as "the rule is not there" (the pitfall documented in ../pane-heads/check.mjs) —
// and measures getComputedStyle over raw CDP.
//
//   npm --prefix console run viewer:font
//   node console/scripts/viewer-font/check.mjs --sizes 13,26
//
// Exit status is the verdict: 0 = every surface takes its own font size.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checker, serveDir, startBrowser } from "../lib/headless.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(HERE, "../..");
const SRC = path.join(ROOT, "src");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const [SMALL, BIG] = arg("sizes", "13,26").split(",").map(Number);
const CHAT = 13.5; // the mirror's default, held fixed while --viewer-size moves
const SHOT = arg("screenshot", "");

// ---------------------------------------------------------------- stylesheets
// Ordered the way ../pane-heads/check.mjs orders them: what `import { App }` pulls in runs
// before main.tsx's own CSS imports, and the ordered ones must keep their order because
// same-named global classes are merged in @import order (viewer.css is only such an index).
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
  // src/marp-themes/* is the one directory that is NOT part of this cascade: MarpView hands
  // those to marp-core, which injects them into its Shadow DOM, and they `@import "default"`
  // — a theme that exists only inside marp-core. Linked here the sheet answers 200 and still
  // fires onerror over that unresolvable import, i.e. it would fail the "everything loaded"
  // guard below for a file that has nothing to do with the page.
  const rest = all.filter((p) => !ordered.includes(p) && !p.includes("/marp-themes/")).sort();
  return [...rest, ...ordered].map((p) => "/" + path.relative(ROOT, p));
}

// ----------------------------------------------------------------------- DOM
// Transcribed from the JSX, including the class each surface wraps MarkdownView in:
//   viewer — FileView.tsx (.fileview + the viewerStyle vars) > parts/FileViewerShell.tsx
//            (.file-viewer-shell > .md-scroll > MarkdownView) ; the .md-sticky overlay is
//            appended to the scroller by parts/mdStickyHeadings.ts
//   mirror — MirrorView.tsx (--chat-size) > .mirror-turn > MarkdownView
//   chat   — parts/ChatMarkdown.tsx inside .chatview
const BODY = `
<div class="fileview" id="viewer" style="--viewer-font: monospace; --viewer-tab: 4">
  <div class="file-viewer-shell">
    <div class="md-scroll">
      <div class="md-sticky"><div class="md-sticky-row md-sticky-h2" id="v-sticky">heading</div></div>
      <div class="markdown" id="v-md">
        <h2 id="v-h2">heading</h2>
        <p id="v-p">prose with <code id="v-code">inline</code> code</p>
        <pre><code id="v-pre">fenced</code></pre>
      </div>
    </div>
  </div>
</div>
<div class="mirror" id="mirror">
  <div class="mirror-turn">
    <div class="markdown">
      <p id="m-p">prose</p>
      <pre><code id="m-pre">fenced</code></pre>
    </div>
  </div>
</div>
<div class="chatview" id="chat" style="font-size: 15px">
  <div class="markdown"><p id="c-p">prose</p></div>
</div>`;

const IDS = ["v-md", "v-p", "v-h2", "v-code", "v-pre", "v-sticky", "m-p", "m-pre", "c-p"];

const { server, requests, port } = await serveDir(path.resolve(ROOT));
const b = await startBrowser();
try {
  // Any path on the origin will do: the page is built here, so nothing is fetched but CSS.
  await b.goto(`http://127.0.0.1:${port}/`);
  const failed = await b.evaluate(`(async () => {
    document.documentElement.setAttribute("data-theme", "dark");
    document.head.innerHTML = "";
    const loaded = ${JSON.stringify(stylesheets())}.map((href) => new Promise((res) => {
      const l = document.createElement("link");
      l.rel = "stylesheet";
      l.href = href;
      l.onload = () => res(null);
      l.onerror = () => res(href);
      document.head.appendChild(l);
    }));
    document.body.innerHTML = ${JSON.stringify(BODY)};
    window.measure = (viewerSize) => {
      document.getElementById("viewer").style.setProperty("--viewer-size", viewerSize + "px");
      document.getElementById("mirror").style.setProperty("--chat-size", ${CHAT} + "px");
      document.body.offsetHeight;
      const out = {};
      for (const id of ${JSON.stringify(IDS)}) {
        out[id] = parseFloat(getComputedStyle(document.getElementById(id)).fontSize);
      }
      return out;
    };
    return (await Promise.all(loaded)).filter(Boolean);
  })()`);

  const small = await b.evaluate(`measure(${SMALL})`);
  const big = await b.evaluate(`measure(${BIG})`);
  if (SHOT) await b.screenshot(SHOT);

  const { check, report } = checker();
  const near = (a, want) => Math.abs(a - want) < 0.5;
  const at = (m, id) => `${id}=${m[id]}px`;

  // The harness itself first: a missing sheet reads as "the rule is not there".
  check(failed.length === 0, "every stylesheet loaded", failed.join(" "));
  check(
    requests.filter((r) => r.status === 404 && r.path.endsWith(".css")).length === 0,
    "no stylesheet 404",
  );

  // The viewer: prose, headings, both kinds of code and the sticky breadcrumb all move.
  check(near(small["v-p"], SMALL) && near(big["v-p"], BIG), "prose takes --viewer-size", `${at(small, "v-p")} ${at(big, "v-p")}`);
  check(big["v-h2"] > small["v-h2"] * 1.5, "heading scales with it", `${at(small, "v-h2")} ${at(big, "v-h2")}`);
  check(big["v-code"] > small["v-code"] * 1.5, "inline code scales with it", `${at(small, "v-code")} ${at(big, "v-code")}`);
  check(big["v-pre"] > small["v-pre"] * 1.5, "fenced code scales with it", `${at(small, "v-pre")} ${at(big, "v-pre")}`);
  // The breadcrumb row carries its own em multiplier per level (.md-sticky-h2 = 1.05em), so
  // what is pinned is the ratio, not the absolute size.
  check(
    near(big["v-sticky"] / small["v-sticky"], BIG / SMALL),
    "sticky heading follows the headings",
    `${at(small, "v-sticky")} ${at(big, "v-sticky")}`,
  );

  // The other two surfaces of the same component are untouched by that setting. The mirror
  // sitting on --chat-size is also the positive control: it proves the harness can see a
  // font size differ at all, so "the viewer moved" is not a measurement artefact.
  check(near(small["m-p"], CHAT) && near(big["m-p"], CHAT), "mirror prose stays on --chat-size", `${at(small, "m-p")} ${at(big, "m-p")}`);
  check(small["m-pre"] === big["m-pre"], "mirror fenced code is not dragged along", `${at(small, "m-pre")} ${at(big, "m-pre")}`);
  check(near(small["c-p"], 15) && near(big["c-p"], 15), "chat prose keeps inheriting its bubble", `${at(small, "c-p")} ${at(big, "c-p")}`);

  console.log(`viewer ${SMALL}px:`, JSON.stringify(small));
  console.log(`viewer ${BIG}px:`, JSON.stringify(big));
  process.exitCode = report();
} finally {
  b.close();
  server.close();
}
