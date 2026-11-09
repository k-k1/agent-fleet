// README screenshot capture — drives the real Console bundle (served by server.mjs
// against fixtures) with headless Chromium over raw CDP, and writes WebP files to docs/img/.
//
//   node console/scripts/shots/capture.mjs [--locale ja|en] [--theme dark|light]
//                                          [--only hero,mirror] [--out <dir>]
//
// No Playwright/Puppeteer: Node 22's global WebSocket speaks CDP directly, matching
// how the rest of this repo drives headless Chromium (docs/log/31, the UI harness note).
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
const FOCUS_TREE = { assistant: 1, workitems: 0, memos: 0, schedules: 0, repos: 1, files: 0 };
const SHOW_TOOLS = { assistant: 1, workitems: 1, memos: 1, schedules: 1, repos: 1, files: 0 };

// Open the Start hub. Right after boot the bar can still read the workspace as not running and
// ask to start it; cancel that and click again once the state has loaded.
const OPEN_START_HUB = `
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      for (let i = 0; i < 10; i++) {
        document.querySelector(".ws-newsession")?.click();
        await wait(700);
        const c = document.querySelector(".ui-confirm");
        if (!c) break;
        [...c.querySelectorAll("button")].find((b) => /キャンセル|Cancel/.test(b.textContent || ""))?.click();
        await wait(1000);
      }`;

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
    // Shoot with the changed-files strip open (docs/log/68). It is collapsed by default, so
    // without this the "files the session edited" surface never appears in the picture.
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
    // WS bar's Start button (.ws-newsession) after boot.
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
    // Settings › Agent memory, for the features page: the memories every agent kind shares,
    // each with the session and agent that wrote it, opened from the account menu like usage.
    name: "memory",
    sections: FOCUS_TREE,
    settings: "memory",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      // Open the pinned project memory so the detail pane shows a body and its author.
      await new Promise((r) => setTimeout(r, 1500));
      document.querySelector(".mem-md-pick")?.click();
    })()`,
    settle: 2000,
  },
  {
    // The member inbox (ADR 0111), for the features page: a request waiting to be taken, one this
    // member holds with the session started from it, and a direct message.
    name: "inbox",
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".inbox-btn")?.click();
      // Unfold the request this member took: its session and the Done / PR controls.
      await new Promise((r) => setTimeout(r, 1000));
      document.querySelectorAll(".inbox-group")[1]?.querySelector(".inbox-card-head")?.click();
    })()`,
    settle: 2000,
  },
  {
    // The session menu, for the features page: move to another agent, hand off, share, and the
    // three Markdown exports of the conversation, in one open menu.
    name: "sessionmenu",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
    })()`,
    settle: 1200,
  },
  {
    // Moving a session to another agent (ADR 0115), for the features page: the dialog opened from
    // the Claude session's menu, with the kinds it can go to and the work that goes with it.
    name: "move",
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /別のエージェントへ移す|Move to another agent/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2000,
  },
  {
    // The image-generation studio (ADR 0081, ADR 0100): the bound claude session's mirror on
    // the left, the studio's draft in the middle (the negative locked), and the trial slot, a
    // batch mid-run, the results and the studio's pictures on the right. The stub's second read
    // of the studio carries one more agent edit (dim light, lanterns), which the pane only picks up
    // on its 2 s poll; the shot waits for it, so the prompt field is outlined as the agent's and the
    // chat's last edit card is matched to its history entry.
    name: "imagegen",
    sections: FOCUS_TREE,
    width: 1800,
    height: 1000,
    layout: {
      cols: [col("c0", [pane("p0", null, { kind: "imagegen", studioId: "5f0c2d1e-8a4b-4c3d-9e2f-1a2b3c4d5e6f" })])],
      colRatios: [1],
      activeId: "p0",
    },
    ready: `[...document.querySelectorAll("textarea")].some((t) => /lanterns/.test(t.value)) &&
      !/どの編集かを特定できません|Could not tell which edit this was/.test(document.body.innerText)`,
  },
  {
    // The file viewer, for the features page on agent-fleet.org: a Markdown design note rendered
    // with its front matter, Mermaid sequence diagram and table, beside the team's draw.io
    // architecture diagram. Mermaid and the draw.io viewer (4 MB, handed to its frame by the
    // page) both load lazily, so the shot waits for both: the sequence diagram's SVG, and the zoom
    // the draw.io pane's header shows once the viewer has drawn (FileHeadControls diagramState).
    name: "files",
    sections: FOCUS_TREE,
    width: 1800,
    height: 1000,
    layout: {
      cols: [
        col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/checkout-flow.md" })]),
        col("c1", [pane("p1", null, { kind: "file", filePath: "repos/webshop/docs/architecture.drawio" })]),
      ],
      colRatios: [0.55, 0.45],
      activeId: "p0",
    },
    ready: `!!document.querySelector(".mermaid-diagram svg") &&
      [...document.querySelectorAll(".fi-meta")].some((el) => /\\d%/.test(el.textContent || ""))`,
  },
  {
    // The work-item inbox, for the features page: GitHub, Jira and Bitbucket rows with the CI and
    // conflict marks, new-activity dots and comment counts on the rows, and the detail panel of a
    // green, mergeable pull request (demo/webshop#315) — its checks, reviews, the Merge button and
    // the buttons that start a session or a review on its head branch.
    name: "workitems",
    sections: { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 0, files: 0 },
    // What the user last saw of two rows (console/src/features/workitems/seen.ts): an older
    // updatedAt and fewer comments, so #312 reads "new comments" and Bitbucket's ledger#201 reads
    // "updated" — the dot and the bold title.
    storage: {
      "af.workitems.seen.v1": JSON.stringify({
        "github:demo/webshop#312": { u: "2020-01-01T00:00:00Z", c: 2 },
        "bitbucket:acme/ledger#201": { u: "2020-01-01T00:00:00Z", c: 7 },
      }),
    },
    width: 1600,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "sessions", showStopped: false })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 400));
      [...document.querySelectorAll(".wi-row")].find((el) => el.textContent.includes("#315"))?.click();
      // Unfold "Review this pull request" so the shot shows how a review starts on the head branch.
      await new Promise((r) => setTimeout(r, 900));
      [...document.querySelectorAll("[role=dialog] details > summary, .modal details > summary")].pop()?.click();
    })()`,
    settle: 1800,
  },
  {
    // Settings › Skills (read-only): the personal skills of one agent kind by scope, with a
    // skill and a command of the same name in two folders (the later one is shown as shadowed)
    // and one opened to show where it lives.
    name: "skills",
    sections: FOCUS_TREE,
    settings: "skills",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      [...document.querySelectorAll(".skills-row.skills-shadowed")][0]?.click();
    })()`,
    settle: 2000,
  },
  {
    // The sessions overview (ADR 0078): every running session as a card, grouped by
    // repository and family. Shot for the user guide (guide/assets/), not the README.
    name: "overview",
    sections: FOCUS_TREE,
    width: 1600,
    height: 700,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "sessions", showStopped: false })])], colRatios: [1], activeId: "p0" },
    settle: 1200,
  },
  {
    // The fleet graph (ADR 0096): one lane per session on a time axis, archived lanes
    // shown, as the pane opens by default. Shot for the user guide (guide/assets/).
    name: "fleetgraph",
    sections: FOCUS_TREE,
    width: 1600,
    height: 620,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "fleetgraph", showArchived: true })])], colRatios: [1], activeId: "p0" },
    settle: 1500,
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
  // ---- user guide scenes (ADR 0131) ----
  // `guide: true` keeps a scene out of the default run, which feeds the READMEs: name it in --only
  // and pass --out. `clip` crops to the union of the selectors' boxes plus `pad`: the component and
  // the control that opens it, nothing around them (a whole bar is noise on a doc page).
  {
    name: "aws-settings",
    guide: true,
    clip: ".settings-modal",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "ssm",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2500,
  },
  {
    // Settings › Issue tracker before Jira is connected: the two ways in.
    name: "tracker-settings",
    guide: true,
    // The tab holds one card, so the whole modal would be mostly blank.
    clip: ".conns",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "tracker",
    width: 1500,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
    })()`,
    settle: 4000,
  },
  {
    // Settings › Chat: a connected Discord card with its notification settings open, Slack not yet.
    name: "chat-settings",
    guide: true,
    stubEnv: { SHOT_CHAT: "1" },
    clip: ".conns",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "chat",
    width: 1500,
    height: 2000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      document.querySelector(".ps-disclosure")?.click();
      // The tab is longer than the modal's fixed height: let the modal grow so nothing scrolls away.
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
    })()`,
    settle: 4000,
  },
  {
    // Settings › Ops & monitoring: PagerDuty, Grafana and CloudWatch connected, AWS not yet.
    name: "ops-settings",
    guide: true,
    stubEnv: { SHOT_OPS: "1" },
    clip: ".conns",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "ops",
    width: 1500,
    height: 1900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      // The tab is longer than the modal's fixed height: let the modal grow so nothing scrolls away.
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
    })()`,
    settle: 4000,
  },
  {
    name: "aws-badge",
    guide: true,
    clip: [".ws-aws-btn", ".ws-aws-pop"],
    pad: 24,
    sections: FOCUS_TREE,
    width: 1400,
    height: 760,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 800));
      document.querySelector(".ws-aws-btn")?.click();
    })()`,
    settle: 1500,
  },
  {
    name: "aws-toast",
    guide: true,
    // The stub serves a pending login request only in a run that sets this (server.mjs).
    stubEnv: { SHOT_AWS_LOGIN_REQUEST: "1" },
    clip: ".ui-toast",
    pad: 12,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    settle: 4000,
  },
  {
    name: "gcp-settings",
    guide: true,
    // The Google Cloud profiles exist only in a run that sets this (server.mjs).
    stubEnv: { SHOT_GCP: "1" },
    clip: ".settings-modal",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "gcp",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2500,
  },
  {
    name: "gcp-badge",
    guide: true,
    stubEnv: { SHOT_GCP: "1" },
    // Wider than aws-badge: with both cloud badges a 1400 px bar folds them behind ⋯.
    clip: [".ws-gcp-btn", ".ws-gcp-pop"],
    pad: 24,
    sections: FOCUS_TREE,
    width: 1800,
    height: 760,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      // The badge appears only once the profiles' login states have loaded.
      for (let i = 0; i < 50 && !document.querySelector(".ws-gcp-btn"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".ws-gcp-btn")?.click();
    })()`,
    settle: 1500,
  },
  {
    name: "gcp-toast",
    guide: true,
    stubEnv: { SHOT_GCP: "1", SHOT_GCP_LOGIN_REQUEST: "1" },
    clip: ".ui-toast",
    pad: 12,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    settle: 6000,
  },
  {
    name: "ssm-hosts",
    guide: true,
    stubEnv: { SHOT_SSM_HOSTS: "1" },
    // The host list is the second group of the tab; the action tags it so the clip can find it.
    clip: "#shot-ssm-hosts",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "ssm",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      const g = [...document.querySelectorAll(".ds-group")].find((el) => /SSM ホスト|SSM hosts/.test(el.querySelector(".ds-title")?.textContent || ""));
      if (g) { g.id = "shot-ssm-hosts"; g.scrollIntoView({ block: "start" }); }
    })()`,
    settle: 2500,
  },
  {
    name: "mcp-settings",
    guide: true,
    // One row per origin plus a remote server signed in with OAuth (server.mjs).
    stubEnv: { SHOT_MCP_GUIDE: "1" },
    clip: ".mcp-list",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "mcp",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // The modal is a fixed 760 px tall; let it grow so the whole list (and the OAuth row's
      // sign-in panel at the bottom) is on screen for the clip.
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "2000px"; m.style.maxHeight = "none"; }
      await new Promise((r) => setTimeout(r, 300));
      document.querySelector(".mcp-list")?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    name: "ssm-start",
    guide: true,
    stubEnv: { SHOT_SSM_HOSTS: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      await wait(800);
      document.querySelector(".ws-newsession")?.click();
      await wait(800);
      [...document.querySelectorAll(".start-row")].find((el) => /SSM/.test(el.textContent || ""))?.click();
      await wait(800);
      [...document.querySelectorAll("button")].find((el) => /AWS から検索|Search AWS/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2500,
  },
  {
    // The studio as the user guide shows it: the same pane as "imagegen", cropped to the pane.
    name: "images-studio",
    guide: true,
    clip: ".igen",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1800,
    height: 1000,
    layout: {
      cols: [col("c0", [pane("p0", null, { kind: "imagegen", studioId: "5f0c2d1e-8a4b-4c3d-9e2f-1a2b3c4d5e6f" })])],
      colRatios: [1],
      activeId: "p0",
    },
    ready: `[...document.querySelectorAll("textarea")].some((t) => /lanterns/.test(t.value)) &&
      !/どの編集かを特定できません|Could not tell which edit this was/.test(document.body.innerText)`,
  },
  {
    // The generation form alone (the studio's middle column), just tall enough for the whole form.
    name: "images-form",
    guide: true,
    clip: ".igen-col-form",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1800,
    height: 1080,
    layout: {
      cols: [col("c0", [pane("p0", null, { kind: "imagegen", studioId: "5f0c2d1e-8a4b-4c3d-9e2f-1a2b3c4d5e6f" })])],
      colRatios: [1],
      activeId: "p0",
    },
    ready: `[...document.querySelectorAll("textarea")].some((t) => /lanterns/.test(t.value))`,
  },
  {
    // The gallery on the image-generation pane's output folder (server.mjs serves it only here).
    name: "images-gallery",
    guide: true,
    stubEnv: { SHOT_GALLERY: "1" },
    clip: ".gal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 760,
    layout: {
      cols: [col("c0", [pane("p0", null, { kind: "gallery", galleryPath: ".cache/agent-fleet/generated/console" })])],
      colRatios: [1],
      activeId: "p0",
    },
    settle: 2500,
  },
  {
    // Settings > Agents: the image-generation switch on, with the provider order under it.
    name: "images-settings",
    guide: true,
    stubEnv: { SHOT_IMAGE_SETTINGS: "1" },
    clip: ["#shot-img-from", "#shot-img-to"],
    pad: 12,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      document.querySelector(".acct-btn")?.click();
      await wait(500);
      [...document.querySelectorAll(".acct-item")].find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await wait(2000);
      // The switch's row and the last note of the provider order bound the clip.
      const label = [...document.querySelectorAll(".settings-modal *")].find((el) =>
        el.children.length === 0 && /^(画像生成|Image generation)$/.test((el.textContent || "").trim()));
      const row = label?.closest(".ds-row") || label?.parentElement;
      if (row) { row.id = "shot-img-from"; row.scrollIntoView({ block: "start" }); }
      let n = row, last = null;
      for (let i = 0; n && i < 4; i++) { n = n.nextElementSibling; if (n?.matches("p.ds-note")) last = n; }
      if (last) last.id = "shot-img-to";
    })()`,
    settle: 2500,
  },
  {
    // Settings › Danger zone › Recreate: the confirmation that says what stays and what goes.
    name: "ws-recreate",
    guide: true,
    stubEnv: { SHOT_WS_GUIDE: "1" },
    clip: ".confirm",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "danger",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      [...document.querySelectorAll(".danger-zone-row .danger-btn")][0]?.click();
    })()`,
    settle: 2500,
  },
  {
    // Settings › Machine: a member's own box, every value measured.
    name: "ws-machine",
    guide: true,
    stubEnv: { SHOT_WS_GUIDE: "1" },
    clip: "#shot-ws-machine",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "machine",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // The tab's first group is the one the page talks about; tag it so the clip can find it.
      const h = [...document.querySelectorAll(".settings-modal .ds-title")].find((el) => /マシン|machine/i.test(el.textContent || ""));
      if (h) h.closest(".ds-group").id = "shot-ws-machine";
    })()`,
    settle: 2500,
  },
  {
    // Settings › Toolchains: the language runtimes, with a newer Java patch to install.
    name: "ws-toolchains",
    guide: true,
    stubEnv: { SHOT_WS_GUIDE: "1" },
    clip: "#shot-ws-toolchains",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "env",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // The tab's first group is the one the page talks about; tag it so the clip can find it.
      const h = [...document.querySelectorAll(".settings-modal .ds-title")].find((el) => /言語ランタイム|runtimes/i.test(el.textContent || ""));
      if (h) h.closest(".ds-group").id = "shot-ws-toolchains";
    })()`,
    settle: 2500,
  },
  {
    // Settings › Databases: Postgres running with two databases, MySQL not installed yet.
    name: "ws-databases",
    guide: true,
    stubEnv: { SHOT_WS_GUIDE: "1" },
    clip: ".settings-modal .display-settings",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "database",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
    })()`,
    settle: 2500,
  },
  {
    // A plan awaiting approval, with the buttons the plans page names.
    name: "conv-plan",
    guide: true,
    stubEnv: { SHOT_PLAN: "1" },
    clip: ".mt-plan",
    pad: 8,
    ready: "!!document.querySelector('.mt-plan-approve')",
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    settle: 2500,
  },
  {
    // The skill / command picker opened from the composer (the session idle, so the composer is free).
    name: "conv-skills",
    guide: true,
    stubEnv: { SHOTS_IDLE: "1" },
    clip: [".mirror-skill-btn", ".mirror-skills"],
    pad: 8,
    ready: "!!document.querySelector('.mirror-skill-btn:not([disabled])')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".mirror-skill-btn")?.click();
    })()`,
    settle: 2000,
  },
  {
    // The session menu's Markdown export: the three choices under their caption.
    name: "conv-export",
    guide: true,
    // The caption and its three items only; the action tags the first and the last.
    clip: ["#shot-export-from", "#shot-export-to"],
    pad: 4,
    ready: "!!document.querySelector('.sess-menu-btn')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      // The rail can still be filling in on a busy host: retry the click until the menu shows.
      let cap;
      for (let i = 0; i < 20 && !cap; i++) {
        const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
          /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
        if (!document.querySelector(".sess-menu")) row?.querySelector(".sess-menu-btn")?.click();
        await new Promise((r) => setTimeout(r, 500));
        cap = [...document.querySelectorAll(".ui-menu-caption")].find((el) => /Markdown/.test(el.textContent || ""));
      }
      if (cap) {
        cap.id = "shot-export-from";
        let last = cap;
        for (let el = cap.nextElementSibling, n = 0; el && n < 3; el = el.nextElementSibling, n++) last = el;
        last.id = "shot-export-to";
      }
    })()`,
    settle: 1200,
  },
  {
    // The live question card with its choices and "Submit answer" (the reply page).
    name: "conv-question",
    guide: true,
    clip: "#shot-question",
    pad: 8,
    ready: "!!document.querySelector('.mt-question')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const q = [...document.querySelectorAll(".mt-question")].pop();
      if (q) { q.id = "shot-question"; q.scrollIntoView({ block: "center" }); }
    })()`,
    settle: 1200,
  },
  {
    // Two follow-ups held in "Waiting to send" while a turn runs, down to the send button and its ▾.
    name: "conv-queue",
    guide: true,
    stubEnv: { SHOT_BUSY: "1" },
    clip: [".mirror-queue", ".mirror-send-split"],
    pad: 8,
    ready: "!!document.querySelector('.mirror-send-more')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const ta = document.querySelector(".mirror-input");
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
      const ja = document.documentElement.lang === "ja" || /送信/.test(document.body.textContent || "");
      const texts = ja
        ? ["エラー文言は messages.ts にまとめて", "終わったら commit して"]
        : ["Keep the error text in messages.ts", "Commit when you are done"];
      for (const t of texts) {
        ta.focus();
        set.call(ta, t);
        ta.dispatchEvent(new Event("input", { bubbles: true }));
        await new Promise((r) => setTimeout(r, 200));
        ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", altKey: true, bubbles: true, cancelable: true }));
        await new Promise((r) => setTimeout(r, 400));
      }
    })()`,
    settle: 1200,
  },
  {
    // The confirmation that "Branch here" opens on the user's own past message.
    name: "conv-fork",
    guide: true,
    stubEnv: { SHOT_FORK: "1" },
    clip: ".ui-modal",
    pad: 0,
    ready: "!!document.querySelector('.mt-fork')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      [...document.querySelectorAll(".mt-fork")].pop()?.click();
    })()`,
    settle: 1500,
  },
  {
    // The "Changed files" strip under the conversation header, opened.
    name: "conv-files",
    guide: true,
    clip: ".mirror-files",
    pad: 0,
    ready: "!!document.querySelector('.mirror-files-toggle')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const s = document.querySelector(".mirror-files");
      if (s && !s.classList.contains("open")) s.querySelector(".mirror-files-toggle")?.click();
    })()`,
    settle: 1500,
  },
  {
    // An answer written in the other language, with "Translate" under it (the translate page).
    name: "conv-translate",
    guide: true,
    stubEnv: { SHOT_TRANSLATE: "1" },
    clip: "#shot-translate",
    pad: 4,
    ready: "!!document.querySelector('.mt-translate')",
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const b = [...document.querySelectorAll(".mt-translate")].pop();
      const turn = b?.closest(".mirror-turn");
      if (turn) { turn.id = "shot-translate"; turn.scrollIntoView({ block: "center" }); }
    })()`,
    settle: 1200,
  },
  {
    // The Start hub opened from the WS bar: where to work (chat, shell, home, a repository).
    name: "sessions-start",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${OPEN_START_HUB}
    })()`,
    settle: 1500,
  },
  {
    // "Start work" for a repository as it opens: agent, model, first prompt, and the folded
    // Location and Details sections with their summaries.
    name: "sessions-launch",
    guide: true,
    // The hub stays open under the dialog; the action tags the top one.
    clip: "#shot-launch",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1800,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${OPEN_START_HUB}
      [...document.querySelectorAll(".start-row")].find((el) => el.textContent.includes("webshop"))?.click();
      await wait(1200);
      const ms = document.querySelectorAll(".ui-modal");
      if (ms.length) ms[ms.length - 1].id = "shot-launch";
    })()`,
    settle: 1500,
  },
  {
    // "Start work" for a repository with its Location section unfolded: new worktree or this copy,
    // the base branch and the working directory.
    name: "sessions-location",
    guide: true,
    clip: ".launch-sec.open",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${OPEN_START_HUB}
      [...document.querySelectorAll(".start-row")].find((el) => el.textContent.includes("webshop"))?.click();
      await wait(1200);
      // The first section in the dialog is Location.
      document.querySelector(".launch-sec-head")?.click();
      await wait(600);
      document.querySelector(".launch-sec.open")?.scrollIntoView({ block: "center" });
    })()`,
    settle: 1800,
  },
  {
    // A GitHub issue's detail opened from the work-item list, with "Start this ticket".
    name: "sessions-issue",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    // The repository section stays open so the repositories have loaded before the detail
    // opens; the start section picks its default repository when it mounts.
    sections: { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 1, files: 0 },
    width: 1400,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 1500));
      [...document.querySelectorAll(".wi-row")].find((el) => el.textContent.includes("#312"))?.click();
    })()`,
    settle: 1800,
  },
  {
    // The issue tracker section of the left pane: CI and conflict marks, unread dots, comment counts.
    name: "sessions-issue-list",
    guide: true,
    clip: "#shot-wi-list",
    pad: 0,
    sections: { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 0, files: 0 },
    storage: {
      "af.workitems.seen.v1": JSON.stringify({
        "github:demo/webshop#312": { u: "2020-01-01T00:00:00Z", c: 2 },
        "bitbucket:acme/ledger#201": { u: "2020-01-01T00:00:00Z", c: 7 },
      }),
    },
    width: 1400,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 800));
      const sec = document.querySelector(".wi-list")?.closest(".ui-section");
      if (sec) sec.id = "shot-wi-list";
    })()`,
    settle: 1200,
  },
  {
    // A mergeable GitHub pull request's detail: live state, checks, reviews and the Merge button.
    name: "sessions-pr",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: { assistant: 0, workitems: 1, memos: 0, schedules: 0, repos: 1, files: 0 },
    width: 1400,
    height: 1700,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 1500));
      [...document.querySelectorAll(".wi-row")].find((el) => el.textContent.includes("#315"))?.click();
    })()`,
    settle: 2500,
  },
  {
    // The session rows of the repository tree, for their state badges.
    name: "sessions-status",
    guide: true,
    clip: "#shot-sess-list",
    pad: 4,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      await new Promise((r) => setTimeout(r, 600));
      const rows = [...document.querySelectorAll(".sess-row")];
      const box = rows[0]?.closest(".sess-list.proj-tree");
      if (box) box.id = "shot-sess-list";
    })()`,
    settle: 1200,
  },
  {
    // The cleanup window, opened from the trash icon in the Repositories heading: the two stages
    // and the candidates with their safety rating.
    name: "sessions-cleanup",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const sel = '[aria-label="掃除を開く（点検・整理）"], [aria-label="Open cleanup (survey & tidy)"]';
      for (let i = 0; i < 50 && !document.querySelector(sel); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(sel)?.click();
      // The window opens with "surveying…" and grows when the candidates arrive: wait for them.
      for (let i = 0; i < 50 && !document.querySelector(".clean-modal .clean-row"); i++) await new Promise((r) => setTimeout(r, 200));
    })()`,
    settle: 2500,
  },
  {
    // "Raise budget & resume" on a session its spend budget stopped (SHOT_SESSIONS_GUIDE).
    name: "sessions-budget",
    guide: true,
    stubEnv: { SHOT_SESSIONS_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /在庫同期バッチの調査|Stock sync batch triage/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /予算を上げて再開|Raise budget/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2000,
  },
  {
    // Stopping every session under one working copy: right-click the webshop row.
    name: "sessions-stop-below",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const find = () => document.querySelector('.repo-card[data-rail-repo="webshop"]');
      for (let i = 0; i < 50 && !find(); i++) await new Promise((r) => setTimeout(r, 200));
      const card = find();
      const b = card.getBoundingClientRect();
      card.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: b.left + 40, clientY: b.top + 10 }));
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll("button, [role=menuitem]")].find((el) => /配下のセッションを停止|Stop the sessions below/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2000,
  },
  {
    // A stopped session opened from the list: the history is read-only until "Resume and continue".
    name: "sessions-resume",
    guide: true,
    clip: ".mirror-compose-resume",
    pad: 4,
    sections: FOCUS_TREE,
    width: 1100,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("so5nw6k")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".mirror-compose-resume"); i++) await new Promise((r) => setTimeout(r, 200));
    })()`,
    settle: 2500,
  },
  {
    // The session menu, for the auto-stop page: Stop when this turn ends and Keep awake.
    name: "sessions-menu",
    guide: true,
    clip: ".sess-menu",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".sess-menu-wrap"); i++) await new Promise((r) => setTimeout(r, 200));
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
    })()`,
    settle: 1200,
  },
  {
    // Moving a session to another agent, for the handoff page (the features-page "move" scene, light and cropped).
    name: "sessions-move",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".sess-menu-wrap"); i++) await new Promise((r) => setTimeout(r, 200));
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /別のエージェントへ移す|Move to another agent/.test(el.textContent || ""))?.click();
      // The dialog mounts a beat after the menu closes: wait for it.
      for (let i = 0; i < 50 && !document.querySelector(".ui-modal"); i++) await new Promise((r) => setTimeout(r, 200));
    })()`,
    settle: 2500,
  },
  {
    // The handoff dialog: the target agent and the optional note.
    name: "sessions-handoff",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".sess-menu-wrap"); i++) await new Promise((r) => setTimeout(r, 200));
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /別のエージェントへ引き継ぐ|Hand off to another agent/.test(el.textContent || ""))?.click();
      // The dialog mounts a beat after the menu closes: wait for it.
      for (let i = 0; i < 50 && !document.querySelector(".ui-modal"); i++) await new Promise((r) => setTimeout(r, 200));
    })()`,
    settle: 2500,
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
const scenes = SCENES.filter((s) => (ONLY ? ONLY.split(",").includes(s.name) : !s.guide));
// A scene's stubEnv changes what the stub serves for the whole run, so it cannot share a run.
if (scenes.some((s) => s.stubEnv) && scenes.length > 1) {
  throw new Error(`run ${scenes.filter((s) => s.stubEnv).map((s) => s.name).join(", ")} on its own (--only)`);
}
const stubEnv = Object.assign({}, ...scenes.map((s) => s.stubEnv || {}));
fs.mkdirSync(OUT, { recursive: true });

const stub = spawn(process.execPath, [path.join(HERE, "server.mjs"), "--port", String(PORT), "--locale", LOCALE], {
  stdio: ["ignore", "inherit", "inherit"],
  env: { ...process.env, ...stubEnv },
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
    // Headless reports a coarse pointer, so `@media (hover: none)` applies and controls that
    // should only appear on hover appear on every row — a picture busier than the real thing
    // (docs/log/80 §80.18.7). Left off by default so the README shot set does not change; pass
    // SHOTS_DESKTOP_HOVER=1 when checking how it looks on the desktop.
    ...(process.env.SHOTS_DESKTOP_HOVER
      ? ["--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"]
      : []),
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
    // (af.layout2.<user>.<tenant> — see console/src/layout/migrate.ts LKEY_NEW). That is
    // the split-mode key, so paneLayout is pinned to "split": under the Tabbed default the
    // Console reads `….tabs`, ignores the scene and shows an unconnected pane.
    const sections = Object.entries(scene.sections || {})
      .map(([id, v]) => `localStorage.setItem("af-section-${id}", "${v}");`)
      .join("\n        ");
    // Per-scene UI state that is remembered in localStorage rather than in the layout —
    // e.g. a disclosure the user has opened before (the mirror's changed-files strip).
    // Without it a shot can only ever show such a panel in its default state.
    const storage = Object.entries(scene.storage || {})
      .map(([k, v]) => `localStorage.setItem(${JSON.stringify(k)}, ${JSON.stringify(String(v))});`)
      .join("\n        ");
    const seed = `
      try {
        ${sections}
        ${storage}
        localStorage.setItem("af-display-settings", ${JSON.stringify(JSON.stringify({ locale: LOCALE, theme: THEME, paneLayout: "split" }))});
        localStorage.setItem("af-tenant", "demo");
        ${scene.settings ? `localStorage.setItem("af-settings-section", ${JSON.stringify(scene.settings)});` : ""}
        localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(scene.layout))});
      } catch (e) {}
    `;
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: seed });
    await cdp.send("Page.navigate", { url: BASE });
    await sleep(4500); // boot + first poll round + xterm paint
    // The first scene of a run can still be on its "Loading…" placeholders after the fixed
    // wait (a cold bundle), and a shot taken then looks like a broken build. Wait for the
    // placeholder text to leave the page, bounded so a genuinely empty pane cannot hang the run.
    for (let i = 0; i < 16; i++) {
      const r = await cdp.send("Runtime.evaluate", {
        expression: `/読み込み中…|Loading…/.test(document.body.innerText)`,
        returnByValue: true,
      });
      if (!r.result?.value) break;
      await sleep(500);
    }
    // A picture drawn after the page is up (Mermaid, the draw.io viewer) names its finished state in
    // scene.ready. The shot waits for it and fails instead of writing a half-drawn picture: the
    // fixed wait above is not enough on a busy host (measured: files-ja came out with the Mermaid
    // source and a blank diagram), and `settle` only applies to scenes with an action.
    if (scene.ready) {
      let ok = false;
      for (let i = 0; i < 60 && !ok; i++) {
        const r = await cdp.send("Runtime.evaluate", { expression: scene.ready, returnByValue: true });
        ok = !!r.result?.value;
        if (!ok) await sleep(500);
      }
      if (!ok) throw new Error(`scene ${scene.name} not ready after 30 s: ${scene.ready}`);
    }
    if (scene.action) {
      await cdp.send("Runtime.evaluate", { expression: scene.action, awaitPromise: true });
      await sleep(scene.settle || 800);
    }
    // The way to settle a question about appearance without judging it by eye (docs/log/80
    // §80.18.7): an expression in scene.measure has its result printed to stdout. Whether
    // paddings or title widths line up is not decided by opinion, only by
    // getBoundingClientRect(). None of the permanent scenes use it.
    if (scene.measure) {
      const m = await cdp.send("Runtime.evaluate", { expression: scene.measure, returnByValue: true });
      console.log("[measure]", scene.name, m.result?.value);
    }

    // WebP straight out of Chromium — no post-processing step, and a fifth of the PNG
    // size at a quality where the UI text stays crisp at 2x.
    let clip;
    if (scene.clip) {
      const sels = [].concat(scene.clip);
      const r = await cdp.send("Runtime.evaluate", {
        returnByValue: true,
        expression: `(() => {
          const bs = ${JSON.stringify(sels)}.map((s) => document.querySelector(s)?.getBoundingClientRect()).filter(Boolean);
          if (bs.length !== ${sels.length}) return null;
          const x = Math.min(...bs.map((b) => b.left)), y = Math.min(...bs.map((b) => b.top));
          return { x, y, w: Math.max(...bs.map((b) => b.right)) - x, h: Math.max(...bs.map((b) => b.bottom)) - y };
        })()`,
      });
      const b = r.result?.value;
      // Fail rather than write an uncropped page: a missing element means the scene is broken.
      if (!b) throw new Error(`scene ${scene.name}: clip ${sels.join(", ")} matched nothing`);
      const pad = scene.pad ?? 16;
      const x = Math.max(0, b.x - pad), y = Math.max(0, b.y - pad);
      clip = { x, y, width: Math.min(scene.width - x, b.w + 2 * pad), height: Math.min(scene.height - y, b.h + 2 * pad), scale: 1 };
    }
    const shot = await cdp.send("Page.captureScreenshot", { format: "webp", quality: 82, captureBeyondViewport: false, ...(clip ? { clip } : {}) });
    const file = path.join(OUT, `${scene.name}-${LOCALE}${THEME === "light" ? "-light" : ""}.webp`);
    fs.writeFileSync(file, Buffer.from(shot.data, "base64"));
    console.log("[shot]", path.relative(ROOT, file), `${scene.width}x${scene.height}@2x`);

    cdp.ws.close();
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
  }
} finally {
  cleanup();
}
