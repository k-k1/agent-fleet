// README screenshot capture — drives the real Console bundle (served by server.mjs
// against fixtures) with headless Chromium over raw CDP, and writes PNGs to docs/img/.
//
//   node console/scripts/shots/capture.mjs [--locale ja|en] [--theme dark|light]
//                                          [--only hero,mirror] [--out <dir>]
//
// No Playwright/Puppeteer: Node 22's global WebSocket speaks CDP directly, matching
// how the rest of this repo drives headless Chromium (docs/31, the UI harness note).
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { DETAIL_SHA } from "./fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const LOCALE = arg("locale", "ja");
const THEME = arg("theme", "dark");
const ONLY = arg("only", "");
const OUT = path.resolve(ROOT, arg("out", "docs/img"));
const PORT = Number(arg("port", 8765));
const CDP_PORT = Number(arg("cdp-port", 9223));
const BASE = `http://127.0.0.1:${PORT}/`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- scenes -----------------------------------------------------------------------
// Each scene pins the pane layout (seeded into localStorage before boot, exactly as a
// returning user's saved split would be) and a viewport.
const pane = (id, session, content) => ({ id, session, content, wrap: null });
const col = (id, panes, rowRatio = 0.5) => ({ id, rowRatio, panes });
const mirrorOf = (s) => pane("p0", s, { kind: "terminal", chat: true });

// Rail sections to force open/closed per scene (localStorage af-section-<id>, see
// console/src/ui/Section.tsx). Keeps the shot focused: the hero shows the repo tree,
// the rail scene shows the global tools.
const FOCUS_TREE = { assistant: 1, memos: 0, schedules: 0, repos: 1, files: 0 };
const SHOW_TOOLS = { assistant: 1, memos: 1, schedules: 1, repos: 1, files: 0 };

const SCENES = [
  {
    name: "console",
    sections: FOCUS_TREE,
    width: 1600,
    height: 980,
    layout: {
      cols: [
        col("c0", [mirrorOf("sk4rq2f")]),
        col("c1", [pane("p1", null, { kind: "scm", scmRepo: "webshop" })]),
      ],
      colRatios: [0.56, 0.44],
      activeId: "p0",
    },
  },
  {
    name: "mirror",
    sections: FOCUS_TREE,
    // 変更ファイル帯を開いた状態で撮る（docs/68）。既定は畳まれているので、開いて
    // おかないと「セッションが直したファイル」の面が絵に出ない。
    storage: { "af.mirror-files-open.sk4rq2f": "1" },
    width: 1280,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
  },
  {
    // Graph on the left, the selected commit's diff on the right — the shape you get
    // by ctrl/⌘-clicking a commit in the graph.
    name: "scm",
    sections: FOCUS_TREE,
    width: 1900,
    height: 900,
    layout: {
      cols: [
        col("c0", [pane("p0", null, { kind: "scm", scmRepo: "webshop" })]),
        col("c1", [pane("p1", null, { kind: "commit", scmRepo: "webshop", commitSha: DETAIL_SHA })]),
      ],
      colRatios: [0.5, 0.5],
      activeId: "p0",
    },
  },
  {
    name: "terminal",
    sections: FOCUS_TREE,
    width: 1280,
    height: 760,
    layout: {
      cols: [col("c0", [pane("p0", "sh2vt8p", { kind: "terminal", chat: false })])],
      colRatios: [1],
      activeId: "p0",
    },
  },
  {
    // The launch hub: one entry point for every agent CLI. Opened by clicking the
    // WS bar's はじめる / Start button (.ws-newsession) after boot.
    name: "launch",
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    // Open the hub, then drill into a repo so the agent picker (every CLI, with the
    // per-agent model choice) is what the shot shows.
    action: `(async () => {
      document.querySelector(".ws-newsession")?.click();
      await new Promise((r) => setTimeout(r, 700));
      const rows = [...document.querySelectorAll(".start-row")];
      rows.find((el) => el.textContent.includes("webshop"))?.click();
    })()`,
    settle: 1400,
  },
  {
    // Settings › workspace › Usage: the per-feature / per-agent / per-model token
    // ledger. Seeded as the remembered section, then opened from the account menu.
    name: "usage",
    sections: FOCUS_TREE,
    settings: "usage",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      // 30 days: the bars are capped at 34px and left-aligned by design, so the 7-day
      // preset leaves most of the plot empty — 30 daily buckets fill it.
      await new Promise((r) => setTimeout(r, 1200));
      const btns = [...document.querySelectorAll("button")];
      btns.find((el) => /^(30日|30 days)$/.test((el.textContent || "").trim()))?.click();
    })()`,
    settle: 2500,
  },
  {
    name: "split",
    sections: SHOW_TOOLS,
    width: 1600,
    height: 980,
    layout: {
      cols: [
        col("c0", [mirrorOf("sk4rq2f")]),
        col("c1", [
          pane("p1", "sh2vt8p", { kind: "terminal", chat: false }),
          pane("p2", null, { kind: "changes", scmRepo: "webshop" }),
        ], 0.45),
      ],
      colRatios: [0.55, 0.45],
      activeId: "p0",
    },
  },
];

// ---- minimal CDP client ------------------------------------------------------------
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (p) {
        this.pending.delete(msg.id);
        msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
      }
    });
  }
  send(method, params = {}) {
    const id = ++this.id;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => {
      ws.addEventListener("open", res, { once: true });
      ws.addEventListener("error", rej, { once: true });
    });
    return new CDP(ws);
  }
}

async function fetchJSON(url, init) {
  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(url, init);
      if (r.ok) return await r.json();
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error(`timeout waiting for ${url}`);
}

// ---- run ---------------------------------------------------------------------------
const scenes = SCENES.filter((s) => !ONLY || ONLY.split(",").includes(s.name));
fs.mkdirSync(OUT, { recursive: true });

const stub = spawn(process.execPath, [path.join(HERE, "server.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "inherit", "inherit"],
});

const chrome = spawn(
  "/usr/bin/chromium",
  [
    "--headless=new",
    "--no-sandbox",
    "--disable-gpu",
    "--hide-scrollbars",
    "--force-device-scale-factor=2",
    "--font-render-hinting=none",
    `--remote-debugging-port=${CDP_PORT}`,
    "--remote-allow-origins=*",
    `--lang=${LOCALE === "ja" ? "ja-JP" : "en-US"}`,
    "about:blank",
  ],
  { stdio: ["ignore", "ignore", "ignore"] },
);

const cleanup = () => {
  try { chrome.kill(); } catch {}
  try { stub.kill(); } catch {}
};
process.on("exit", cleanup);
process.on("SIGINT", () => { cleanup(); process.exit(1); });

try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);
  await fetchJSON(`${BASE}api/whoami`);

  for (const scene of scenes) {
    const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
    const cdp = await CDP.connect(target.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    await cdp.send("Emulation.setDeviceMetricsOverride", {
      width: scene.width,
      height: scene.height,
      deviceScaleFactor: 2,
      mobile: false,
    });
    // Seed the browser state a returning user would have: display settings (locale +
    // theme) and this scene's saved pane layout, keyed exactly like the app writes it
    // (af.layout2.<user>.<tenant> — see console/src/layout/migrate.ts LKEY_NEW).
    const sections = Object.entries(scene.sections || {})
      .map(([id, v]) => `localStorage.setItem("af-section-${id}", "${v}");`)
      .join("\n        ");
    // Per-scene UI state that is remembered in localStorage rather than in the layout —
    // e.g. a disclosure the user has opened before (the mirror's 変更ファイル strip).
    // Without it a shot can only ever show such a panel in its default state.
    const storage = Object.entries(scene.storage || {})
      .map(([k, v]) => `localStorage.setItem(${JSON.stringify(k)}, ${JSON.stringify(String(v))});`)
      .join("\n        ");
    const seed = `
      try {
        ${sections}
        ${storage}
        localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE, theme: THEME }))});
        localStorage.setItem("af-tenant", "demo");
        ${scene.settings ? `localStorage.setItem("af-settings-section", ${JSON.stringify(scene.settings)});` : ""}
        localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(scene.layout))});
      } catch (e) {}
    `;
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: seed });
    await cdp.send("Page.navigate", { url: BASE });
    await sleep(4500); // boot + first poll round + xterm paint
    if (scene.action) {
      await cdp.send("Runtime.evaluate", { expression: scene.action, awaitPromise: true });
      await sleep(scene.settle || 800);
    }

    // WebP straight out of Chromium — no post-processing step, and a fifth of the PNG
    // size at a quality where the UI text stays crisp at 2x.
    const shot = await cdp.send("Page.captureScreenshot", { format: "webp", quality: 82, captureBeyondViewport: false });
    const file = path.join(OUT, `${scene.name}-${LOCALE}${THEME === "light" ? "-light" : ""}.webp`);
    fs.writeFileSync(file, Buffer.from(shot.data, "base64"));
    console.log("[shot]", path.relative(ROOT, file), `${scene.width}x${scene.height}@2x`);

    cdp.ws.close();
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
  }
} finally {
  cleanup();
}
