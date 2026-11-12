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

// Opens Tenant settings from the account menu and picks one rail item by its label (ja|en).
const openTenantSection = (label) => `
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /テナント設定|Tenant settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      [...document.querySelectorAll(".settings-rail button")].find((el) => ${label}.test((el.textContent || "").trim()))?.click();
      await new Promise((r) => setTimeout(r, 1500));`;

// Tenant settings for the guide's admin resource pages (SHOT_ADMIN_RES): open the modal from the
// account menu and pick a rail item. The modal grows to its content so a clip below the fold is
// on screen.
const openTenant = (rail) => `
  document.querySelector(".acct-btn")?.click();
  await new Promise((r) => setTimeout(r, 500));
  [...document.querySelectorAll(".acct-item")].find((el) => /テナント設定|Tenant settings/.test(el.textContent || ""))?.click();
  for (let i = 0; i < 50 && !document.querySelector(".tenant-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
  [...document.querySelectorAll(".tenant-modal .settings-rail-item")].find((b) => ${rail}.test(b.textContent || ""))?.click();
  await new Promise((r) => setTimeout(r, 1500));
  const modal = document.querySelector(".tenant-modal");
  if (modal) { modal.style.height = "auto"; modal.style.maxHeight = "none"; }
`;
// The member detail of aoi-tanaka with only the sections `keep` picks left visible, each tagged
// shot-<n> in order so the clip can name them.
const openMember = (keep) => `${openTenant("/^(メンバー|Members)$/")}
  [...document.querySelectorAll(".member-card")].find((c) => /aoi-tanaka/.test(c.textContent || ""))?.click();
  for (let i = 0; i < 50 && !document.querySelector(".member-detail .ws-size"); i++) await new Promise((r) => setTimeout(r, 200));
  await new Promise((r) => setTimeout(r, 2500));
  const secs = [...document.querySelectorAll(".member-detail > section")];
  const keep = (${keep})(secs);
  secs.forEach((sec) => { if (!keep.includes(sec)) sec.style.display = "none"; });
  keep.forEach((sec, i) => { sec.id = "shot-" + i; });
`;
const ADMIN_RES_ENV = { SHOTS_ADMIN: "1", SHOT_ADMIN_RES: "1" };
const ADMIN_RES_BASE = {
  guide: true,
  stubEnv: ADMIN_RES_ENV,
  pad: 0,
  sections: FOCUS_TREE,
  width: 1500,
  height: 1800,
  layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
  settle: 2000,
};

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
    // The fleet pages of the user guide: the sessions overview pane alone, cropped to the pane.
    name: "fleet-overview",
    guide: true,
    clip: ".ovw",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 760,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "sessions", showStopped: false })])], colRatios: [1], activeId: "p0" },
    settle: 1500,
  },
  {
    // The fleet graph pane alone, archived lanes shown as it opens by default.
    name: "fleet-graph",
    guide: true,
    clip: ".fgraph",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 560,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "fleetgraph", showArchived: true })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 75 && !document.querySelector(".fgraph"); i++) await new Promise((r) => setTimeout(r, 200));
    })()`,
    settle: 2000,
  },
  {
    // The working-set switcher at the top of the left pane, opened, with two sets (SHOT_FLEET_GUIDE).
    name: "fleet-wset",
    guide: true,
    stubEnv: { SHOT_FLEET_GUIDE: "1" },
    clip: [".wset-bar", ".wset-menu"],
    pad: 8,
    sections: FOCUS_TREE,
    width: 1300,
    height: 700,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".wset-btn"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".wset-btn")?.click();
    })()`,
    settle: 1200,
  },
  {
    // Settings › Agents › Session: peer messaging, starting sessions from a session, PR feedback
    // and the child limit, all on (SHOT_FLEET_GUIDE).
    name: "fleet-settings",
    guide: true,
    stubEnv: { SHOT_FLEET_GUIDE: "1" },
    clip: ["#shot-fleet-from", "#shot-fleet-to"],
    pad: 12,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      document.querySelector(".acct-btn")?.click();
      await wait(500);
      [...document.querySelectorAll(".acct-item")].find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await wait(2000);
      const rowOf = (re) => {
        const label = [...document.querySelectorAll(".settings-modal *")].find((el) =>
          el.children.length === 0 && re.test((el.textContent || "").trim()));
        return label?.closest(".ds-row") || label?.parentElement;
      };
      const from = rowOf(/^(セッション間メッセージ|Messages between sessions|Cross-session messaging)$/);
      const limit = rowOf(/^(子セッションの同時本数|Children per session)$/);
      if (from) { from.id = "shot-fleet-from"; from.scrollIntoView({ block: "start" }); }
      const note = limit?.nextElementSibling;
      if (note?.matches("p.ds-note")) note.id = "shot-fleet-to"; else if (limit) limit.id = "shot-fleet-to";
    })()`,
    settle: 2500,
  },
  {
    // The new-chat picker of the Assistant section: where the Fleet Operator is chosen.
    name: "fleet-operator",
    guide: true,
    stubEnv: { SHOT_FLEET_GUIDE: "1" },
    clip: [".assistant-picker-wrap", ".assistant-picker"],
    pad: 8,
    sections: FOCUS_TREE,
    width: 1300,
    height: 700,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".assistant-picker-wrap button"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".assistant-picker-wrap button")?.click();
    })()`,
    settle: 1200,
  },
  {
    // A session report card in the operator's conversation, with the operator's follow-up (SHOT_FLEET_GUIDE).
    name: "fleet-report",
    guide: true,
    stubEnv: { SHOT_FLEET_GUIDE: "1" },
    clip: [".chat-msg.role-report", "#shot-fleet-after"],
    pad: 8,
    sections: FOCUS_TREE,
    width: 1300,
    height: 900,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "chat", conversationId: "c-ops-1", draftAssistantId: null })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".chat-msg.role-report"); i++) await new Promise((r) => setTimeout(r, 200));
      const r = document.querySelector(".chat-msg.role-report");
      const next = r?.nextElementSibling;
      if (next) next.id = "shot-fleet-after";
      r?.scrollIntoView({ block: "center" });
    })()`,
    settle: 1500,
  },
  {
    // A schedule's "Details & edit" dialog, opened from the row menu in the Schedules section (SHOT_FLEET_GUIDE).
    name: "fleet-schedule",
    guide: true,
    stubEnv: { SHOT_FLEET_GUIDE: "1" },
    clip: ".sched-detail-modal",
    pad: 0,
    sections: { ...FOCUS_TREE, schedules: 1 },
    width: 1300,
    height: 1500,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      for (let i = 0; i < 50 && !document.querySelector('.sched-row[data-sched-id="sch-1"] .sched-menu-btn'); i++) await wait(200);
      document.querySelector('.sched-row[data-sched-id="sch-1"] .sched-menu-btn')?.click();
      await wait(500);
      [...document.querySelectorAll(".sched-menu .ui-menu-item")].find((el) => /詳細・編集|Details/.test(el.textContent || ""))?.click();
    })()`,
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
    name: "ws-agent-silent",
    guide: true,
    // The stub reports a silent Agent only in a run that sets this (server.mjs).
    stubEnv: { SHOT_AGENT_SILENT: "1" },
    clip: [".ws-agentsilent .ws-stale-pill", ".ws-agentsilent .ws-stale-pop"],
    pad: 24,
    sections: FOCUS_TREE,
    width: 1400,
    height: 760,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      // The badge appears after the first workspace poll: wait for it rather than for a fixed time.
      for (let i = 0; i < 50 && !document.querySelector(".ws-agentsilent .ws-stale-pill"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".ws-agentsilent .ws-stale-pill")?.click();
    })()`,
    settle: 1500,
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
  {
    // Settings › Git hosting for the user guide's code pages: GitHub not connected yet (its two
    // ways in), Bitbucket connected with its commit identity, and the saved Subversion servers.
    name: "code-git-hosting",
    guide: true,
    stubEnv: { SHOT_CODE_GUIDE: "1" },
    clip: ".conns",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "git",
    width: 1500,
    height: 1800,
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
    settle: 3000,
  },
  {
    // Settings › Internal repos with two repositories: one the member may rename and delete, one
    // created by someone else (no rename / delete buttons).
    name: "code-internal-repos",
    guide: true,
    stubEnv: { SHOT_CODE_GUIDE: "1" },
    clip: ".conns",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "internalrepos",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
    })()`,
    settle: 3000,
  },
  {
    // The rail's "Add" dialog on its Git kind: a Bitbucket repository picked, GitHub not connected.
    name: "code-clone",
    guide: true,
    stubEnv: { SHOT_CODE_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = async (f) => { for (let i = 0; i < 50 && !f(); i++) await new Promise((r) => setTimeout(r, 200)); return f(); };
      const add = await wait(() => document.querySelector('button.ui-btn[title="追加"], button.ui-btn[title="Add"]'));
      add?.click();
      await wait(() => document.querySelector('.repopicker .branch-item[title="acme/payments-api"]'));
      await new Promise((r) => setTimeout(r, 1500));
      document.querySelector('.repopicker .branch-item[title="acme/payments-api"]')?.click();
    })()`,
    settle: 2500,
  },
  {
    // The Repositories rail and its badges: uncommitted, ahead / behind origin, a worktree
    // diverged from its parent, running and stopped session counts.
    name: "code-repo-rows",
    guide: true,
    clip: "#shot-code-repos",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const rows = () => [...document.querySelectorAll(".repo-row")];
      for (let i = 0; i < 50 && rows().length < 2; i++) await new Promise((r) => setTimeout(r, 200));
      const all = rows();
      // Tag the smallest element holding every repository row, so the clip is the list and nothing else.
      let box = all[0];
      while (box && !all.every((r) => box.contains(r))) box = box.parentElement;
      if (box) box.id = "shot-code-repos";
    })()`,
    settle: 1500,
  },
  {
    // A worktree row's right-click menu.
    name: "code-repo-menu",
    guide: true,
    clip: ".ui-menu",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const find = () => [...document.querySelectorAll(".repo-row")].find((el) => /checkout-validation/.test(el.textContent || ""));
      for (let i = 0; i < 50 && !find(); i++) await new Promise((r) => setTimeout(r, 200));
      const row = find();
      const b = row?.getBoundingClientRect();
      row?.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: b ? b.left + 40 : 200, clientY: b ? b.top + 8 : 300 }));
    })()`,
    settle: 1500,
  },
  {
    // The Changes pane: two files staged, the rest not, and a commit message typed.
    name: "code-changes",
    guide: true,
    clip: ".scmview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1100,
    height: 560,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "changes", scmRepo: "webshop" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".commitbox textarea"); i++) await new Promise((r) => setTimeout(r, 200));
      const ta = document.querySelector(".commitbox textarea");
      if (ta) {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set.call(ta, document.documentElement.lang === "ja" ? "チェックアウトの入力検証を追加" : "Add checkout input validation");
        ta.dispatchEvent(new Event("input", { bubbles: true }));
      }
    })()`,
    settle: 1500,
  },
  {
    // "Add a repository" on its Subversion kind: the checkout form.
    name: "code-svn",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = async (f) => { for (let i = 0; i < 50 && !f(); i++) await new Promise((r) => setTimeout(r, 200)); return f(); };
      const add = await wait(() => document.querySelector('button.ui-btn[title="追加"], button.ui-btn[title="Add"]'));
      add?.click();
      const svn = await wait(() => [...document.querySelectorAll(".ui-modal button")].find((el) => (el.textContent || "").trim() === "Subversion"));
      svn?.click();
      await new Promise((r) => setTimeout(r, 300));
      // Fill the form the way a reader would; "Save credentials" appears only once a username is typed.
      const set = (el, v) => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      const ins = [...document.querySelectorAll(".ui-modal input:not([type=checkbox])")];
      if (ins[0]) set(ins[0], "https://svn.example.com/design");
      if (ins[1]) set(ins[1], "trunk");
      if (ins[2]) set(ins[2], "taro");
    })()`,
    settle: 1500,
  },
  {
    // Initialize Git Flow, opened from a repository row's menu: develop exists only on origin, so
    // the dialog shows its origin-only note under the field.
    name: "code-gitflow",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const find = () => [...document.querySelectorAll(".repo-row")].find((el) => /platform-infra/.test(el.textContent || ""));
      for (let i = 0; i < 50 && !find(); i++) await new Promise((r) => setTimeout(r, 200));
      const row = find();
      row?.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 200, clientY: 300 }));
      await new Promise((r) => setTimeout(r, 600));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /Git Flow/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2500,
  },
  {
    // Settings › Agents › the Claude Code card for the user guide's agent pages: connected, with the
    // account, plan and expiry, and Re-authenticate / Disconnect. Behavior stays closed: open, the
    // card is taller than the modal.
    name: "agents-claude",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="claude"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="claude"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // Settings › Agent instructions: where the instructions go, one row per agent.
    name: "agents-instructions",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: ".settings-modal .instr-targets",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "instructions",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      document.querySelector(".settings-modal .instr-targets")?.scrollIntoView({ block: "end" });
    })()`,
    settle: 2500,
  },

  {
    // Settings › Agents › the Behavior block of one card (GitHub Copilot), open: the launch defaults
    // every card shares.
    name: "agents-behavior",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="copilot"] .p-settings',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      const card = document.querySelector('.p-card[data-settings-target="copilot"]');
      card?.querySelector(".ps-disclosure")?.click();
      await new Promise((r) => setTimeout(r, 600));
      card?.querySelector(".p-settings")?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The Codex card before connecting: the ChatGPT subscription and API key choices.
    name: "agents-codex",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="codex"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="codex"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The GitHub Copilot card once GitHub is connected: no sign-in of its own, just the status.
    name: "agents-copilot",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="copilot"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="copilot"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The OpenCode card from its header to the billing routes, Go chosen (the whole card is taller
    // than the modal).
    name: "agents-opencode",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: ['.p-card[data-settings-target="opencode"] .p-head', '.p-card[data-settings-target="opencode"] [role=radiogroup]'],
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="opencode"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The Cursor card before signing in.
    name: "agents-cursor",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="cursor"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="cursor"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The Kiro card before its on-demand install.
    name: "agents-kiro",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="kiro"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="kiro"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The Antigravity card before connecting: the experimental-slot label and OAuth.
    name: "agents-agy",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="agy"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="agy"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The Muse Code card before its one-time install.
    name: "agents-muse",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="muse"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="muse"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The llama.cpp card pointed at a member's own llama-server, reachable.
    name: "agents-lcpp",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: '.p-card[data-settings-target="lcpp"]',
    pad: 0,
    sections: FOCUS_TREE,
    settings: "agents",
    width: 1500,
    height: 2200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1800));
      // The cards render only after the connection check; on a busy host that can outlast the wait.
      for (let i = 0; i < 40 && /接続を確認しています|Checking the connection/.test(document.querySelector(".settings-modal")?.textContent || ""); i++) {
        await new Promise((r) => setTimeout(r, 500));
      }
      document.querySelector('.p-card[data-settings-target="lcpp"]')?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // Settings › Agent usage over 30 days, cropped to the tab.
    name: "agents-usage",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: ".settings-modal .settings-content",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "usage",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      const btns = [...document.querySelectorAll("button")];
      btns.find((el) => /^(30 ?日|30 days)$/.test((el.textContent || "").trim()))?.click();
    })()`,
    settle: 2500,
  },
  {
    // Settings › Skills with Claude's skills under its real config folder, one shadowed.
    name: "agents-skills",
    guide: true,
    stubEnv: { SHOT_AGENTS_GUIDE: "1" },
    clip: ".settings-modal .settings-content",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "skills",
    width: 1500,
    height: 1400,
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
    // The Files section with a folder's right-click menu (files/browse page): open a shell here,
    // download as zip, and the rest. The clip is the menu and the row it was opened on.
    name: "files-menu",
    guide: true,
    clip: [".files-ctxmenu", "#shot-files-row"],
    pad: 8,
    sections: { assistant: 0, workitems: 0, memos: 0, schedules: 0, repos: 0, files: 1 },
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/runbook.md" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      const row = (name) => [...document.querySelectorAll(".fsrow")].find((el) => (el.querySelector(".fs-name")?.textContent || "").trim() === name);
      for (let i = 0; i < 50 && !row("webshop"); i++) await wait(200);
      if (!row("docs")) row("webshop")?.click();
      for (let i = 0; i < 50 && !row("docs"); i++) await wait(200);
      const r = row("docs");
      if (r) r.id = "shot-files-row";
      const b = r?.getBoundingClientRect();
      r?.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: b ? b.left + 150 : 200, clientY: b ? b.top + 8 : 300 }));
    })()`,
    settle: 1500,
  },
  {
    // A Markdown file in the viewer (files/view page): the Preview / Source switch over the
    // rendered text and its Mermaid diagram.
    name: "files-view",
    guide: true,
    clip: ".fileview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1100,
    height: 760,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/checkout-flow.md" })])], colRatios: [1], activeId: "p0" },
    ready: `!!document.querySelector(".mermaid-diagram svg")`,
  },
  {
    // A draw.io diagram in the viewer (files/view page): the Diagram / Source switch and the
    // page and zoom readout the header shows once the viewer has drawn.
    name: "files-drawio",
    guide: true,
    clip: ".fileview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1100,
    height: 700,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/architecture.drawio" })])], colRatios: [1], activeId: "p0" },
    ready: `[...document.querySelectorAll(".fi-meta")].some((el) => /\\d%/.test(el.textContent || ""))`,
  },
  {
    // The same file switched to Split (files/edit page): the editor beside the preview, with Save
    // and AI proposal in the header.
    name: "files-edit",
    guide: true,
    stubEnv: { SHOT_FILES_GUIDE: "1" },
    clip: ".fileview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 760,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/checkout-flow.md" })])], colRatios: [1], activeId: "p0" },
    ready: `!!document.querySelector(".mermaid-diagram svg")`,
    action: `(async () => {
      const b = [...document.querySelectorAll(".fileview button")].find((el) => /^(分割|Split)$/.test((el.textContent || "").trim()));
      b?.click();
    })()`,
    settle: 2500,
  },
  {
    // Text selected in the viewer and "Send" pressed (files/send page): the send dialog.
    name: "files-send",
    guide: true,
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "file", filePath: "repos/webshop/docs/checkout-flow.md" })])], colRatios: [1], activeId: "p0" },
    ready: `!!document.querySelector(".mermaid-diagram svg")`,
    // The pill appears for a selection in the source lines (the code grid), not in the rendered
    // preview, so the scene switches to Source first and selects a few lines there.
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      [...document.querySelectorAll(".fileview button")].find((el) => /^(ソース|Source)$/.test((el.textContent || "").trim()))?.click();
      for (let i = 0; i < 30 && !document.querySelector(".codeview .codegrid"); i++) await wait(200);
      const grid = document.querySelector(".codeview .codegrid");
      const cells = grid ? [...grid.querySelectorAll("*")].filter((el) => /カートから|from the cart to/.test(el.textContent || "") && el.children.length === 0) : [];
      const cell = cells[0];
      if (cell) {
        const range = document.createRange();
        range.selectNodeContents(cell);
        const sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
        const b = cell.getBoundingClientRect();
        cell.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, clientX: b.right - 10, clientY: b.bottom - 4 }));
      }
      for (let i = 0; i < 20 && !document.querySelector(".sel-send-pill"); i++) await wait(200);
      document.querySelector(".sel-send-pill")?.click();
    })()`,
    settle: 2000,
  },
  {
    // Settings › Display, the Terminal group (files/terminal page): font, size and the copy and
    // paste switches.
    name: "files-terminal",
    guide: true,
    clip: "#shot-term-group",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "display",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      const h = [...document.querySelectorAll(".settings-modal .ds-title")].find((el) => /^(ターミナル|Terminal)$/.test((el.textContent || "").trim()));
      if (h) { const g = h.closest(".ds-group") || h.parentElement; g.id = "shot-term-group"; g.scrollIntoView({ block: "center" }); }
    })()`,
    settle: 2500,
  },
  {
    // The workspace bar's Preview popover (preview/browser-pane page): the port and path fields,
    // the open targets and this start's preview URLs.
    name: "preview-pop",
    guide: true,
    clip: ".ws-preview-pop",
    pad: 4,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      for (let i = 0; i < 30 && document.querySelector(".ws-preview-btn")?.disabled; i++) await wait(300);
      document.querySelector(".ws-preview-btn")?.click();
      for (let i = 0; i < 30 && !document.querySelector(".ws-preview-pop .pv-host"); i++) await wait(200);
      // A port typed in, so the open buttons read as they do in use rather than greyed out.
      const port = document.querySelector(".ws-preview-pop .preview-port");
      if (port) {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(port, "5173");
        port.dispatchEvent(new Event("input", { bubbles: true }));
      }
    })()`,
    settle: 1500,
  },
  {
    // Settings › Preview subdomains (preview/subdomains page): the current URLs, the ports and
    // the switches that widen who may open them.
    name: "preview-settings",
    guide: true,
    stubEnv: { SHOT_PREVIEW_GUIDE: "1" },
    clip: ["#shot-pv-a", "#shot-pv-b"],
    pad: 4,
    sections: FOCUS_TREE,
    settings: "preview",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // The tab is one section; the clip runs from its title to the reissue note.
      const h = document.querySelector(".settings-modal .ds-title");
      if (h) h.id = "shot-pv-a";
      const re = [...document.querySelectorAll(".settings-modal button")].find((el) => /いま捨てて発行し直す|Discard and mint a new one/i.test(el.textContent || ""));
      if (re) re.id = "shot-pv-b";
      h?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  {
    // The development OIDC IdP block of the same tab (preview/dev-signin page): issuer, discovery
    // URL and client id with their copy buttons, and the secret rotation.
    name: "preview-devidp",
    guide: true,
    stubEnv: { SHOT_PREVIEW_GUIDE: "1" },
    clip: ["#shot-dip-a", "#shot-dip-b"],
    pad: 4,
    sections: FOCUS_TREE,
    settings: "preview",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      const h = [...document.querySelectorAll(".settings-modal .ds-title")].find((el) => /OIDC/.test(el.textContent || ""));
      if (h) h.id = "shot-dip-a";
      const rot = [...document.querySelectorAll(".settings-modal button")].find((el) => /新しい secret を発行|Issue a new secret/i.test(el.textContent || ""));
      const row = rot?.closest(".ds-row, div");
      if (row) row.id = "shot-dip-b";
      h?.scrollIntoView({ block: "start" });
    })()`,
    settle: 2500,
  },
  // ---- admin pages (SHOT_ADMIN_GUIDE + SHOTS_ADMIN) ----
  // Each opens Tenant settings on one section through the account menu, then tags the element to
  // crop as #shot-admin (the panels share their class names with every other settings tab).
  ...[
    { name: "admin-roster", section: "members", clip: [".member-summary", ".member-grid"], target: "null" },
    { name: "admin-add-member", section: "members", target: `document.querySelector("form.add-member")` },
    {
      name: "admin-member-ops",
      section: "members",
      // The detail's resources and sessions come first, so the operations panel sits low.
      height: 2600,
      open: `[...document.querySelectorAll(".member-card")].find((el) => /ren-sato/.test(el.textContent || ""))?.click();`,
      target: `[...document.querySelectorAll(".settings-content .admin-panel")].find((el) => /^(操作|Operations)$/.test(el.querySelector("h4")?.textContent?.trim() || ""))`,
    },
    { name: "admin-signin-methods", section: "signin", target: `document.querySelector(".settings-content .admin-panel")` },
    { name: "admin-networks", section: "network", target: `document.querySelector(".settings-content .admin-panel")` },
    { name: "admin-oauth-github", section: "git-oauth", target: `document.querySelector(".settings-content .admin-panel")` },
    { name: "admin-branch-rules", section: "branch-rules", target: `document.querySelector(".settings-content .admin-panel")` },
    { name: "admin-member-defaults", section: "member-defaults", target: `document.querySelector(".settings-content .admin-panel")` },
  ].map((a) => ({
    name: a.name,
    guide: true,
    stubEnv: { SHOT_ADMIN_GUIDE: "1", SHOTS_ADMIN: "1" },
    clip: a.clip || "#shot-admin",
    pad: 12,
    sections: FOCUS_TREE,
    width: 1500,
    height: a.height || 1600,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      localStorage.setItem("af-tenant-section", ${JSON.stringify(a.section)});
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /テナント設定|Tenant settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      ${a.open || ""}
      await new Promise((r) => setTimeout(r, 1500));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
      const t = ${a.target};
      if (t) t.id = "shot-admin";
      // A text box shows its whole content rather than a scrolled window of it.
      for (const ta of document.querySelectorAll(".settings-content textarea")) ta.style.height = ta.scrollHeight + 8 + "px";
    })()`,
    settle: 2500,
  })),
  // ---- operate pages: sign-in (SHOT_OPERATE_SIGNIN) ----
  {
    // Admin › Sign-in method register: what the deployment administrator approves, with one row
    // waiting and its "Approve and activate" button.
    name: "operate-signin-register",
    guide: true,
    stubEnv: { SHOT_OPERATE_SIGNIN: "1" },
    clip: "#shot-register",
    pad: 12,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    storage: { "af-admin-place": JSON.stringify({ root: "register", scope: null, scopeSection: "limits" }) },
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      const p = document.querySelector(".admin-modal .admin-panel");
      if (p) p.id = "shot-register";
    })()`,
    settle: 2000,
  },
  // ---- team pages (SHOT_TEAM_GUIDE) ----
  {
    // The hand-to-member dialog: two teammates the session is shared with, and a branch with
    // commits not pushed yet, so the push gate and "Push, then send" show.
    name: "team-handover",
    guide: true,
    stubEnv: { SHOT_TEAM_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".sess-menu-wrap"); i++) await new Promise((r) => setTimeout(r, 200));
      // The rail re-renders while its first polls land, which closes a menu opened too early.
      await new Promise((r) => setTimeout(r, 3000));
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /メンバーへ引き継ぐ|Hand off to a member/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".ui-modal"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 1500));
      // Fill the title and the prompt the way the owner would, through React's own setter.
      const set = (el, v) => {
        const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        Object.getOwnPropertyDescriptor(proto, "value").set.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      const m = document.querySelector(".ui-modal");
      const ja = document.documentElement.lang !== "en" && /引き継/.test(m?.textContent || "");
      const t = m?.querySelector("input[type=text], input:not([type])");
      if (t && !t.value) set(t, ja ? "チェックアウトの入力検証（続き）" : "Checkout input validation (cont.)");
      const p = m?.querySelector("textarea");
      if (p) set(p, ja ? "郵便番号の検証まで済んでいます。残りは電話番号の形式チェックとテストです。" : "Postal-code validation is done. Phone-number format checks and their tests are left.");
      // Tick "Push, then send" so the send button is live, as it is right before sending.
      m?.querySelector("input[type=checkbox]")?.click();
    })()`,
    settle: 1500,
  },
  {
    // The share dialog opened from a session's menu: the exposure warning, a teammate picked
    // from the suggestions, and "Can propose" chosen.
    name: "team-share",
    guide: true,
    stubEnv: { SHOT_TEAM_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".sess-menu-wrap"); i++) await new Promise((r) => setTimeout(r, 200));
      // The rail re-renders while its first polls land, which closes a menu opened too early.
      await new Promise((r) => setTimeout(r, 3000));
      const row = [...document.querySelectorAll(".sess-menu-wrap")].find((el) =>
        /チェックアウトの入力検証|Checkout input validation/.test(el.closest("li, .sess-row, [class*=row]")?.textContent || ""));
      row?.querySelector(".sess-menu-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".ui-menu-item")].find((el) => /^\\s*(共有する|Share)/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".share-recipient-suggest .ui-menu-item"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".share-recipient-suggest .ui-menu-item")?.click();
      await new Promise((r) => setTimeout(r, 300));
      [...document.querySelectorAll(".ui-modal .seg-btn")][1]?.click();
    })()`,
    settle: 1200,
  },
  {
    // The inbox for the team pages: the received tab with a request waiting, one this member took
    // (unfolded, with its session) and a message. The features-page "inbox" scene, light and cropped.
    name: "team-inbox",
    guide: true,
    clip: ".inbox-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".inbox-btn"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 3000));
      document.querySelector(".inbox-btn")?.click();
      for (let i = 0; i < 50 && !document.querySelector(".inbox-modal"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 1000));
      document.querySelectorAll(".inbox-group")[1]?.querySelector(".inbox-card-head")?.click();
    })()`,
    settle: 2000,
  },
  {
    // Settings › Limit board: this member shows bands to the whole tenant; two teammates publish
    // and one shows nothing (read as Unknown).
    name: "team-limits",
    guide: true,
    stubEnv: { SHOT_TEAM_GUIDE: "1" },
    // The tab content only: the settings rail beside it is noise on this page.
    clip: ".settings-content",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "limits",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
    })()`,
    settle: 2500,
  },
    {
    // The inbox's compose tab: a request with a recipe attached.
    name: "team-inbox-compose",
    guide: true,
    stubEnv: { SHOT_TEAM_GUIDE: "1" },
    clip: ".inbox-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1600,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".inbox-btn"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 3000));
      document.querySelector(".inbox-btn")?.click();
      for (let i = 0; i < 50 && !document.querySelector(".inbox-modal"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 1000));
      [...document.querySelectorAll(".inbox-modal button")].find((el) => /^\\s*(新規|New)\\s*$/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 800));
      const box = [...document.querySelectorAll(".inbox-modal label")].find((el) => /レシピを添付する|Attach a recipe/.test(el.textContent || ""))?.querySelector("input[type=checkbox]");
      box?.click();
    })()`,
    settle: 1500,
  },
// ---- end team pages ----
  // ---- chat pages (SHOT_CHATPAGES_GUIDE) ----
  {
    // The assistant chat: the agent chip, the work plan open and the context gauge past 80 %
    // with its Compact button.
    name: "chatpages-chat",
    guide: true,
    stubEnv: { SHOT_CHATPAGES_GUIDE: "1" },
    clip: ".chatview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 700,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "chat", conversationId: "c-pay-1" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".chat-plan-toggle"); i++) await new Promise((r) => setTimeout(r, 200));
      if (!document.querySelector(".chat-plan")) document.querySelector(".chat-plan-toggle")?.click();
    })()`,
    settle: 2500,
  },
  {
    // The rail's "+" (new chat): the three builtins, an assistant of the member's own, and Create.
    name: "chatpages-picker",
    guide: true,
    stubEnv: { SHOT_CHATPAGES_GUIDE: "1" },
    clip: [".assistant-picker", "#shot-asst-head"],
    pad: 6,
    sections: FOCUS_TREE,
    width: 1300,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".assistant-picker-wrap button"); i++) await new Promise((r) => setTimeout(r, 200));
      const wrap = document.querySelector(".assistant-picker-wrap");
      const head = wrap?.closest(".ui-section-head");
      if (head) head.id = "shot-asst-head";
      wrap?.querySelector("button")?.click();
    })()`,
    settle: 1500,
  },
  {
    // The create-assistant dialog, empty, so its placeholders show a translator as the example.
    name: "chatpages-create",
    guide: true,
    stubEnv: { SHOT_CHATPAGES_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && !document.querySelector(".assistant-picker-wrap button"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".assistant-picker-wrap button")?.click();
      await new Promise((r) => setTimeout(r, 600));
      [...document.querySelectorAll(".assistant-picker .ui-menu-item")].find((el) => /アシスタントを作成|Create assistant/.test(el.textContent || ""))?.click();
    })()`,
    settle: 2000,
  },
  {
    // The memo queue with two memos selected: the selection bar and its Send button.
    name: "chatpages-memos",
    guide: true,
    stubEnv: { SHOT_CHATPAGES_GUIDE: "1" },
    clip: "#shot-memos",
    pad: 0,
    sections: { assistant: 0, workitems: 0, memos: 1, schedules: 0, repos: 0, files: 0 },
    width: 1300,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && document.querySelectorAll(".memo-row").length < 4; i++) await new Promise((r) => setTimeout(r, 200));
      const rows = [...document.querySelectorAll(".memo-row")];
      rows.slice(0, 2).forEach((r) => r.querySelector("input[type=checkbox]")?.click());
      await new Promise((r) => setTimeout(r, 400));
      const sec = document.querySelector(".memo-row")?.closest(".ui-section");
      if (sec) sec.id = "shot-memos";
    })()`,
    settle: 1200,
  },
  {
    // The send dialog for those two memos: the editable text and the destinations.
    name: "chatpages-memo-send",
    guide: true,
    stubEnv: { SHOT_CHATPAGES_GUIDE: "1" },
    clip: ".ui-modal",
    pad: 0,
    sections: { assistant: 0, workitems: 0, memos: 1, schedules: 0, repos: 0, files: 0 },
    width: 1300,
    height: 1300,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      for (let i = 0; i < 50 && document.querySelectorAll(".memo-row").length < 4; i++) await new Promise((r) => setTimeout(r, 200));
      const rows = [...document.querySelectorAll(".memo-row")];
      rows.slice(0, 2).forEach((r) => r.querySelector("input[type=checkbox]")?.click());
      await new Promise((r) => setTimeout(r, 400));
      document.querySelector(".memo-selbar .ui-btn-primary, .memo-selbar button:last-child")?.click();
    })()`,
    settle: 2000,
  },
  // ---- end chat pages ----
  // ---- outside-access and security pages (SHOT_OUTSIDE_GUIDE) ----
  {
    // Settings › MCP tokens & apps right after "Issue token": the once-shown token with its
    // .mcp.json template, above the form it came from.
    name: "outside-token-issued",
    guide: true,
    stubEnv: { SHOT_OUTSIDE_GUIDE: "1" },
    clip: [".pat-issued", ".pat-form"],
    pad: 12,
    sections: FOCUS_TREE,
    settings: "tokens",
    width: 1500,
    height: 1600,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "1500px"; m.style.maxHeight = "none"; }
      [...document.querySelectorAll(".pat-form button")].find((b) => /トークンを発行|Issue token/.test(b.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1200));
      // The template is built from the page's own origin, which here is the stub's loopback port;
      // show the address a deployment would have instead.
      const pre = document.querySelector(".pat-tmpl");
      if (pre) pre.textContent = pre.textContent.replace(location.origin, "https://console.example.com");
      document.querySelector(".pat-issued")?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1500,
  },
  {
    // The connected apps under the tokens: claude.ai on the memory endpoint and a command-line
    // client on /mcp, the second with its recent calls open.
    name: "outside-apps",
    guide: true,
    stubEnv: { SHOT_OUTSIDE_GUIDE: "1" },
    clip: "[data-shot=apps]",
    pad: 12,
    sections: FOCUS_TREE,
    settings: "tokens",
    width: 1500,
    height: 2400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "2300px"; m.style.maxHeight = "none"; }
      const apps = document.querySelector(".display-settings > section");
      apps?.setAttribute("data-shot", "apps");
      const toggles = apps ? [...apps.querySelectorAll(".calls > button")] : [];
      toggles[1]?.click();
      await new Promise((r) => setTimeout(r, 1200));
      apps?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1500,
  },
  // ---- end outside-access and security pages ----
  // ---- settings pages (SHOT_SETTINGS_GUIDE) ----
  {
    // Settings › Display: the tab in the dialog's rail, with language, time zone, layout and theme.
    name: "settings-display",
    guide: true,
    stubEnv: { SHOT_SETTINGS_GUIDE: "1" },
    clip: ".settings-modal",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "display",
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
    // Settings › Notifications: the table of notification kinds and what each does on arrival.
    name: "settings-notifications",
    guide: true,
    stubEnv: { SHOT_SETTINGS_GUIDE: "1" },
    clip: "#shot-settings-noti",
    pad: 0,
    sections: FOCUS_TREE,
    settings: "notifications",
    width: 1500,
    height: 1600,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // The table is longer than the modal's fixed height: let the modal grow so nothing scrolls away.
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
      const g = document.querySelector(".settings-modal .noti-table")?.closest(".ds-group");
      if (g) g.id = "shot-settings-noti";
    })()`,
    settle: 2500,
  },
  {
    // Settings › Read aloud with speech turned on: the switch and the voice group under it.
    name: "settings-tts",
    guide: true,
    stubEnv: { SHOT_SETTINGS_GUIDE: "1" },
    clip: ["#shot-tts-from", "#shot-tts-to"],
    pad: 0,
    sections: FOCUS_TREE,
    settings: "tts",
    width: 1500,
    height: 2000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1500));
      // Turn speech on: the first group's first switch, its "On" button.
      const first = document.querySelector(".settings-modal .display-settings .ds-group");
      [...(first?.querySelectorAll("button") || [])].find((b) => /^(オン|On)$/.test((b.textContent || "").trim()))?.click();
      await new Promise((r) => setTimeout(r, 1000));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
      // From the speech switch down to the reading language's note: the voice choices a reader
      // sets first. The rest of the tab is a long list of options the page names in a table.
      const groups = document.querySelectorAll(".settings-modal .display-settings .ds-group");
      if (groups[0]) groups[0].id = "shot-tts-from";
      const lang = [...(groups[1]?.querySelectorAll(".ds-row") || [])].find((el) => /読み上げ言語|Read-aloud language/i.test(el.textContent || ""));
      const note = lang?.nextElementSibling;
      if (note) note.id = "shot-tts-to";
      // The grown modal is centred, so everything below the clip would push its top off screen:
      // hide what follows the note.
      let n = note?.nextElementSibling;
      while (n) { n.style.display = "none"; n = n.nextElementSibling; }
      [...groups].slice(2).forEach((g) => { g.style.display = "none"; });
      document.querySelector(".settings-modal .ds-reset")?.style.setProperty("display", "none");
    })()`,
    settle: 2500,
  },
  // ---- end settings pages ----
  // ---- admin resource pages (SHOT_ADMIN_RES) ----
  {
    // Tenant settings › Limits & idle as a tenant administrator sees it: read-only figures.
    ...ADMIN_RES_BASE,
    name: "admin-res-limits",
    clip: ".tenant-summary",
    action: `(async () => {${openTenant("/上限・自動停止|Limits/")}})()`,
  },
  {
    // Tenant settings › Engine access: chat restricted to ticked members, images open to all.
    ...ADMIN_RES_BASE,
    name: "admin-res-engine-access",
    clip: ".engine-access",
    action: `(async () => {${openTenant("/推論エンジンの利用者|Engine access/")}})()`,
  },
  {
    // Member detail: the resource meters and the size card below them.
    ...ADMIN_RES_BASE,
    name: "admin-res-member",
    clip: ["#shot-0", "#shot-1"],
    action: `(async () => {${openMember('(secs) => secs.filter((x) => x.classList.contains("ws-size") || (x.nextElementSibling && x.nextElementSibling.classList && x.nextElementSibling.classList.contains("ws-size")))')}})()`,
  },
  {
    // Member detail: the size editor opened with Change.
    ...ADMIN_RES_BASE,
    name: "admin-res-size",
    clip: "#shot-0",
    action: `(async () => {${openMember('(secs) => secs.filter((x) => x.classList.contains("ws-size"))')}
      document.querySelector("#shot-0 .ws-size-edit")?.click();
      await new Promise((r) => setTimeout(r, 800));
    })()`,
  },
  {
    // Member detail: the idle auto-stop outlook held by a working session and a pin.
    ...ADMIN_RES_BASE,
    name: "admin-res-idle",
    clip: ["#shot-0"],
    action: `(async () => {${openMember("(secs) => secs.slice(0, 1)")}})()`,
  },
  {
    // Member detail: Operations, with force-stop above the irreversible actions.
    ...ADMIN_RES_BASE,
    name: "admin-res-stop",
    clip: ["#shot-0"],
    action: `(async () => {${openMember('(secs) => secs.filter((x) => x.querySelector(".danger-zone"))')}})()`,
  },
  // ---- end admin resource pages ----
  // ---- operate pages: day-to-day operations (SHOT_OPERATE_RUN) ----
  {
    // Admin › Deployment › Appearance: the colour swatches, the label and the preview a
    // deployment administrator sets to tell staging from production.
    name: "operate-brand",
    guide: true,
    stubEnv: { SHOT_OPERATE_RUN: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(見た目|Appearance)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .swatch"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".admin-modal .admin-panel")?.setAttribute("id", "shot-operate");
    })()`,
    settle: 1500,
  },
  // ---- operate pages: security (SHOT_OPERATE_SECURE) ----
  {
    // Admin › Deployment › Egress: the mode switch in log-only, a member's proposal waiting for
    // approval, the added allowlist with the built-in Control Plane host, and the observed destinations.
    name: "operate-egress",
    guide: true,
    stubEnv: { SHOT_OPERATE_SECURE: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1600,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(通信|Egress)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .adm-egress-row"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 800));
      const m = document.querySelector(".admin-modal .settings-content") || document.querySelector(".admin-modal");
      if (m) { m.style.maxHeight = "none"; m.style.height = "auto"; }
      (document.querySelector(".admin-modal .admin-stage") || document.querySelector(".admin-modal .admin-panel"))?.setAttribute("id", "shot-operate");
    })()`,
    settle: 1500,
  },
  {
    // Admin › Deployment › MCP sign-in hosts: a saved list with host-only entries, so the warning
    // that recommends URL prefixes shows, and Reset to default.
    name: "operate-mcp-trusted",
    guide: true,
    stubEnv: { SHOT_OPERATE_SECURE: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(MCP ログイン許可ホスト|MCP sign-in hosts)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal textarea"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 800));
      const m = document.querySelector(".admin-modal .settings-content") || document.querySelector(".admin-modal");
      if (m) { m.style.maxHeight = "none"; m.style.height = "auto"; }
      (document.querySelector(".admin-modal .admin-stage") || document.querySelector(".admin-modal .admin-panel"))?.setAttribute("id", "shot-operate");
    })()`,
    settle: 1500,
  },
  {
    // Admin › Deployment › Dev sign-in: the switch on, the issuer, the number of workspaces holding
    // a client and the warning never to trust the issuer in production.
    name: "operate-dev-idp",
    guide: true,
    stubEnv: { SHOT_OPERATE_SECURE: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(開発用ログイン|Dev sign-in)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal input[type=checkbox]"); i++) await new Promise((r) => setTimeout(r, 200));
      await new Promise((r) => setTimeout(r, 800));
      const m = document.querySelector(".admin-modal .settings-content") || document.querySelector(".admin-modal");
      if (m) { m.style.maxHeight = "none"; m.style.height = "auto"; }
      (document.querySelector(".admin-modal .admin-stage") || document.querySelector(".admin-modal .admin-panel"))?.setAttribute("id", "shot-operate");
    })()`,
    settle: 1500,
  },
  {
    // Admin › Slots (operate/aws-ecs/slot-maintenance): an outdated slot with its automatic
    // replacement, a reserved one, a wedged one with Stop (force), and the bulk reservation.
    name: "operate-slots",
    guide: true,
    stubEnv: { SHOT_OPERATE_KEYS: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      for (let i = 0; i < 50 && ![...document.querySelectorAll(".admin-modal .settings-rail-item")].some((b) => /^(スロット|Slots)$/.test((b.textContent || "").trim())); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(スロット|Slots)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .pool-table"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".admin-modal .pool-view .admin-panel")?.setAttribute("id", "shot-operate");
    })()`,
    settle: 1500,
  },
  {
    // Admin › Inference engines (SHOT_OPERATE_GPU): the chat engine's row — the mode, what is
    // running and until when, the idle stop and a GPU class left above the default.
    name: "operate-gpu-engine",
    guide: true,
    stubEnv: { SHOT_OPERATE_GPU: "1" },
    clip: "#shot-operate-gpu",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1300,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(推論エンジン|Inference engines)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .engines-class"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".admin-modal .admin-panel")?.setAttribute("id", "shot-operate-gpu");
    })()`,
    settle: 1500,
  },
  {
    // The same row's 14-day history, opened, with a blank (unrecorded) stretch on it.
    name: "operate-gpu-history",
    guide: true,
    stubEnv: { SHOT_OPERATE_GPU: "1" },
    clip: "#shot-operate-gpu",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1500,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(推論エンジン|Inference engines)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .engines-history"); i++) await new Promise((r) => setTimeout(r, 200));
      const d = document.querySelector(".admin-modal .engines-history");
      // Only the history: the rest of the row is the operate-gpu-engine picture, and with it
      // above, the opened grid runs past the bottom of the modal.
      for (const el of d.parentElement.children) if (el !== d) el.style.display = "none";
      d.open = true;
      d.style.width = "fit-content";
      d.setAttribute("id", "shot-operate-gpu");
    })()`,
    settle: 2500,
  },
  {
    // The model catalogue opened from the chat engine's row, on its search face: three invented
    // GGUF repositories, one gated, each with its Add button.
    name: "operate-gpu-catalog",
    guide: true,
    stubEnv: { SHOT_OPERATE_GPU: "1" },
    clip: ".engine-catalog-pane",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1300,
    height: 600,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "engineAdd", engineKey: "llm", lora: false, view: "search" })])], colRatios: [1], activeId: "p0" },
    settle: 2500,
  },
  {
    // Admin › Read aloud: the speech engine's mode and state, and the shared reading dictionary.
    name: "operate-gpu-tts",
    guide: true,
    stubEnv: { SHOT_OPERATE_GPU: "1" },
    clip: "#shot-operate-gpu",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1100,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(読み上げ|Read aloud)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .ds-userdict"); i++) await new Promise((r) => setTimeout(r, 200));
      document.querySelector(".admin-modal .admin-stage")?.setAttribute("id", "shot-operate-gpu");
    })()`,
    settle: 1500,
  },
  // ---- operate pages: engines outside the fleet (SHOT_OPERATE_ENGINES) ----
  ...[
    // Admin › Inference engines: the LAN ComfyUI panel (URL, write-only key, what removing falls back to).
    ["operate-engines-comfy-lan", '[data-testid="comfy-lan-panel"]'],
    // Admin › Inference engines: the lender's form that shows a borrowing deployment its token.
    ["operate-engines-issue-token", ".engines-issue"],
  ].map(([name, target]) => ({
    name,
    guide: true,
    stubEnv: { SHOT_OPERATE_ENGINES: "1" },
    clip: "#shot-operate",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      [...document.querySelectorAll(".acct-item")].find((el) => /^\\s*(管理|Admin)\\s*$/.test(el.textContent || ""))?.click();
      for (let i = 0; i < 50 && !document.querySelector(".admin-modal .settings-rail-item"); i++) await new Promise((r) => setTimeout(r, 200));
      [...document.querySelectorAll(".admin-modal .settings-rail-item")].find((b) => /^(推論エンジン|Inference engines)$/.test((b.textContent || "").trim()))?.click();
      for (let i = 0; i < 50 && !document.querySelector(${JSON.stringify(target)}); i++) await new Promise((r) => setTimeout(r, 200));
      const m = document.querySelector(".admin-modal");
      if (m) { m.style.height = "1300px"; m.style.maxHeight = "none"; }
      const el = document.querySelector(${JSON.stringify(target)});
      el?.setAttribute("id", "shot-operate");
      el?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1500,
  })),
  // ---- end operate pages ----
  // ---- memory import/export pages (SHOT_MEMIO_GUIDE) ----
  {
    // Settings › Agent memory › Import & export: the hub of cards, grouped Import / Export /
    // Maintenance, each with its Start button.
    name: "memio-hub",
    guide: true,
    stubEnv: { SHOT_MEMIO_GUIDE: "1" },
    clip: "#shot-memio-hub",
    pad: 8,
    sections: FOCUS_TREE,
    settings: "memory",
    width: 1500,
    height: 1400,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      [...document.querySelectorAll(".mem-viewnav-tab")].find((el) => /取り込み・書き出し|Import & export/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 1200));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "1300px"; m.style.maxHeight = "none"; }
      const panel = document.querySelector(".mem-ie-group")?.parentElement;
      panel?.setAttribute("id", "shot-memio-hub");
      panel?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1500,
  },
  {
    // Settings › Memory snapshots › History: the status line with Snapshot now, the history with its
    // trigger badges (two Agent Fleet memory writes folded into one line) and the newest diff.
    name: "memio-snapshots",
    guide: true,
    stubEnv: { SHOT_MEMIO_GUIDE: "1" },
    clip: ".mem-tab",
    pad: 8,
    sections: FOCUS_TREE,
    settings: "memorysnapshots",
    width: 1500,
    height: 1200,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      [...document.querySelectorAll(".mem-viewnav-tab")].find((el) => /^(履歴|History)/.test((el.textContent || "").trim()))?.click();
      await new Promise((r) => setTimeout(r, 1200));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "1100px"; m.style.maxHeight = "none"; }
    })()`,
    settle: 1500,
  },
  {
    // Settings › Agent memory › Settings: only the outside-access choice, its note and the
    // "How to connect" link (the rest of the view belongs to the memory settings page).
    name: "memio-external",
    guide: true,
    stubEnv: { SHOT_MEMIO_GUIDE: "1" },
    clip: "#shot-memio-ext",
    pad: 8,
    sections: FOCUS_TREE,
    settings: "memory",
    width: 1500,
    height: 2000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.querySelector(".acct-btn")?.click();
      await new Promise((r) => setTimeout(r, 500));
      const items = [...document.querySelectorAll(".acct-item")];
      items.find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 2000));
      [...document.querySelectorAll(".mem-viewnav-tab")].find((el) => /^(設定|Settings)/.test((el.textContent || "").trim()))?.click();
      await new Promise((r) => setTimeout(r, 1200));
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "1900px"; m.style.maxHeight = "none"; }
      const label = [...document.querySelectorAll(".ds-label")].find((el) =>
        /^(Agent Fleet の外からメモリを使わせる|Use Agent Fleet memory from outside Agent Fleet)$/.test((el.textContent || "").trim()));
      const row = label?.closest(".ds-row");
      if (row) {
        // The row, its note and the "How to connect" link are siblings; wrap the three to clip them.
        const note = row.nextElementSibling;
        const link = note?.nextElementSibling;
        const wrap = document.createElement("div");
        wrap.id = "shot-memio-ext";
        row.before(wrap);
        wrap.append(row);
        if (note) wrap.append(note);
        if (link) wrap.append(link);
        wrap.scrollIntoView({ block: "start" });
      }
    })()`,
    settle: 1500,
  },
  // ---- end memory import/export pages ----
  // ---- tenant administration pages (guide/admin/) ----
  {
    // Tenant settings › Operations › Audit: the toolbar and the newest rows.
    name: "admin-audit",
    guide: true,
    stubEnv: { SHOTS_ADMIN: "1", SHOT_ADMIN_OUTSIDE: "1" },
    clip: ".audit-view",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${openTenantSection("/^(監査|Audit)$/")}
    })()`,
    settle: 1000,
  },
  {
    // Tenant settings › Operations › Running time: the six-month trend with the change per month.
    name: "admin-usage-monthly",
    guide: true,
    stubEnv: { SHOTS_ADMIN: "1", SHOT_ADMIN_OUTSIDE: "1" },
    clip: ".usage-view .monthly-trend",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1200,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${openTenantSection("/^(稼働時間|Running time)$/")}
      document.querySelector(".usage-view .monthly-trend")?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1000,
  },
  {
    // Tenant settings › Operations › MCP distribution: the add form with a URL filled in and
    // "members enter the value" ticked.
    name: "admin-mcp-form",
    guide: true,
    stubEnv: { SHOTS_ADMIN: "1", SHOT_ADMIN_OUTSIDE: "1" },
    clip: ".adm-mcp-form",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1400,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${openTenantSection("/^(MCP 配布|MCP distribution)$/")}
      [...document.querySelectorAll(".mcp-admin button")].find((el) => /配布する MCP サーバーを追加|Distribute an MCP server/.test(el.textContent || ""))?.click();
      await new Promise((r) => setTimeout(r, 800));
      const form = document.querySelector(".adm-mcp-form");
      const setVal = (el, v) => {
        const proto = Object.getPrototypeOf(el);
        Object.getOwnPropertyDescriptor(proto, "value").set.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      const inputs = [...(form?.querySelectorAll("input.cinput") || [])];
      if (inputs[0]) setVal(inputs[0], /配布/.test(document.body.textContent || "") ? "チーム Wiki" : "Team wiki");
      await new Promise((r) => setTimeout(r, 300));
      const url = form?.querySelector('input[placeholder="https://"]');
      if (url) setVal(url, "https://wiki.example.com/mcp");
      await new Promise((r) => setTimeout(r, 300));
      const box = [...(form?.querySelectorAll("label") || [])].find((el) => /認証の値は各メンバーが入力する|Each member enters the credential/.test(el.textContent || ""));
      box?.querySelector("input")?.click();
      await new Promise((r) => setTimeout(r, 500));
      form?.scrollIntoView({ block: "start" });
    })()`,
    settle: 1000,
  },
  {
    // Tenant settings › Operations › External memory access: the allow/block switch.
    name: "admin-ext-memory",
    guide: true,
    stubEnv: { SHOTS_ADMIN: "1", SHOT_ADMIN_OUTSIDE: "1" },
    clip: ".ext-memory",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${openTenantSection("/^(外部からのメモリ利用|External memory access)$/")}
    })()`,
    settle: 1000,
  },
  {
    // Tenant settings › Operations › Watch sources: the switch in its initial "block" state.
    name: "admin-watch-sources",
    guide: true,
    stubEnv: { SHOTS_ADMIN: "1", SHOT_ADMIN_OUTSIDE: "1" },
    clip: ".watch-src",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
    action: `(async () => {${openTenantSection("/^(ウォッチのソース|Watch sources)$/")}
    })()`,
    settle: 1000,
  },
  // ---- end tenant administration pages ----
  // ---- getting-started pages (guide/user/start/) ----
  {
    // Home on a first-run member: the welcome card with the workspace started and no agent yet.
    name: "start-welcome",
    guide: true,
    stubEnv: { SHOT_START_GUIDE: "firstrun" },
    clip: ".home-setup",
    // The card waits for the workspace state and the connection probes; on a busy host that outlasts the boot wait.
    ready: `!!document.querySelector(".home-setup")`,
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "home" })])], colRatios: [1], activeId: "p0" },
  },
  {
    // The whole Console once, for the screen tour: rail, bar, a session beside the commit graph.
    name: "start-tour",
    guide: true,
    // Wait for the workspace to read as running and both panes to finish loading.
    ready: `!!document.querySelector(".ws-power.on") && !/読み込み中|Loading/.test(document.body.innerText)`,
    sections: SHOW_TOOLS,
    width: 1440,
    height: 860,
    layout: {
      cols: [
        col("c0", [mirrorOf("sk4rq2f")]),
        col("c1", [pane("p1", null, { kind: "scm", scmRepo: "webshop" })]),
      ],
      colRatios: [0.58, 0.42],
      activeId: "p0",
    },
  },
  {
    // The command palette as it opens: the sessions list, waiting ones first.
    name: "start-palette",
    guide: true,
    // Wait for the bar to read the workspace as running; before that the panes and the palette are empty.
    ready: `!!document.querySelector(".ws-power.on")`,
    clip: ".cp-panel",
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      document.body.focus();
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "p", code: "KeyP", ctrlKey: true, bubbles: true }));
    })()`,
    settle: 1200,
  },
  {
    // The top bar's Appearance menu with its main-area layout switch.
    name: "start-appearance",
    guide: true,
    // Wait for the bar to read the workspace as running; before that the panes and the palette are empty.
    ready: `!!document.querySelector(".ws-power.on")`,
    clip: [".appr-btn", ".appr-menu"],
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => { document.querySelector(".appr-btn")?.click(); })()`,
    settle: 800,
  },
  {
    // The Console on a phone: one session, the bar folded into its menu.
    name: "start-phone",
    guide: true,
    // Wait for the bar to read the workspace as running; before that the panes and the palette are empty.
    ready: `!!document.querySelector(".ws-power.on")`,
    mobile: true,
    sections: FOCUS_TREE,
    width: 390,
    height: 760,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
  },
  {
    // The bar's "Restart needed" badge with its popover open (SHOT_START_GUIDE=stale).
    name: "start-restart",
    guide: true,
    stubEnv: { SHOT_START_GUIDE: "stale" },
    clip: [".ws-stale-pill", ".ws-stale-pop"],
    // The badge appears only once the bar has read the workspace as running.
    ready: `!!document.querySelector(".ws-stale-pill")`,
    pad: 8,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => { document.querySelector(".ws-stale-pill")?.click(); })()`,
    settle: 800,
  },
  // ---- the memory pages (SHOT_MEMORY_GUIDE) ----
  // One webshop project and the user scope, the same memories in every scene (fixtures.mjs
  // memoryGuide*). The three Memory pane scenes open the pane on one view each; the settings and
  // lint scenes open Settings › Agent memory.
  {
    // The Memory pane's Memories view: the list, and a project memory opened with its actions.
    name: "memory-view",
    guide: true,
    stubEnv: { SHOT_MEMORY_GUIDE: "1" },
    clip: ".mview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 1000,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "memory", memoryView: "memories" })])], colRatios: [1], activeId: "p0" },
    ready: `!!document.querySelector(".mem-md-pick")`,
    action: `(async () => {
      [...document.querySelectorAll(".mem-md-pick")].find((b) => /checkout-tests-need-payment-mock/.test(b.getAttribute("data-row-key") || b.textContent || ""))?.click();
    })()`,
    settle: 1500,
  },
  {
    // The Memory pane's Changes view: the newest change selected, its diff and the two ways back.
    name: "memory-changes",
    guide: true,
    stubEnv: { SHOT_MEMORY_GUIDE: "1" },
    clip: ".mview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 900,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "memory", memoryView: "changes" })])], colRatios: [1], activeId: "p0" },
    // The newest change is selected on arrival; wait for its diff rather than a fixed time.
    ready: `!!document.querySelector(".mem-diff-head button") && !/読み込み中|Loading/.test(document.querySelector(".mem-diff")?.textContent || "")`,
  },
  {
    // Settings › Agent memory › Settings: every switch on, with the tidy-up's status.
    name: "memory-settings",
    guide: true,
    stubEnv: { SHOT_MEMORY_GUIDE: "1" },
    clip: ".mem-tab",
    pad: 12,
    sections: FOCUS_TREE,
    settings: "memory",
    width: 1500,
    height: 2000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      document.querySelector(".acct-btn")?.click();
      await wait(500);
      [...document.querySelectorAll(".acct-item")].find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await wait(1500);
      document.querySelector("#mem-af-tab-settings")?.click();
      // The view is longer than the modal's fixed height: let the modal grow so nothing scrolls away.
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
    })()`,
    settle: 2500,
  },
  {
    // Settings › Agent memory › Import & export › the lint report, on the webshop project.
    name: "memory-lint",
    guide: true,
    stubEnv: { SHOT_MEMORY_GUIDE: "1" },
    clip: ".mem-wiz",
    pad: 12,
    sections: FOCUS_TREE,
    settings: "memory",
    width: 1500,
    height: 2000,
    layout: { cols: [col("c0", [mirrorOf("sk4rq2f")])], colRatios: [1], activeId: "p0" },
    action: `(async () => {
      const wait = (ms) => new Promise((r) => setTimeout(r, ms));
      document.querySelector(".acct-btn")?.click();
      await wait(500);
      [...document.querySelectorAll(".acct-item")].find((el) => /設定|Settings/.test(el.textContent || ""))?.click();
      await wait(1500);
      document.querySelector("#mem-af-tab-importexport")?.click();
      await wait(800);
      [...document.querySelectorAll(".mem-ie-card button")].find((b) => /リント|lint/i.test(b.getAttribute("aria-label") || ""))?.click();
      await wait(1200);
      // A React-controlled select: set the value through the native setter so onChange fires.
      const sel = document.querySelector(".mem-wiz .mem-scope select");
      if (sel) {
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set.call(sel, "p-webshop");
        sel.dispatchEvent(new Event("change", { bubbles: true }));
      }
      const m = document.querySelector(".settings-modal");
      if (m) { m.style.height = "auto"; m.style.maxHeight = "none"; }
    })()`,
    settle: 2500,
  },
  {
    // The Memory pane's Proposals view: three pending proposals, the first opened with its preview.
    name: "memory-proposal",
    guide: true,
    stubEnv: { SHOT_MEMORY_GUIDE: "1" },
    clip: ".mview",
    pad: 0,
    sections: FOCUS_TREE,
    width: 1400,
    height: 880,
    layout: { cols: [col("c0", [pane("p0", null, { kind: "memory", memoryView: "proposals" })])], colRatios: [1], activeId: "p0" },
    ready: `!!document.querySelector(".mem-cs-actions button")`,
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
      mobile: !!scene.mobile,
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
