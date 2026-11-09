// Stub Control Plane for the README screenshot harness.
//
// Serves the real console bundle (console/dist) plus just enough of the CP's API
// surface, answered from fixtures.mjs, for the Console to render a populated fleet
// with no backend, no Docker and no real data. Unknown /api paths answer {} and are
// logged, so a missing endpoint shows up as a log line instead of a hung view.
//
//   node console/scripts/shots/server.mjs [--port 8765] [--locale ja] [--demo <scenario>]
import http from "node:http";
import zlib from "node:zlib";
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { fileURLToPath } from "node:url";
import * as fx from "./fixtures.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const DIST = path.resolve(HERE, "../../dist");

const argv = process.argv.slice(2);
const arg = (name, dflt) => {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : dflt;
};
const PORT = Number(arg("port", 8765));
const LOCALE = arg("locale", "ja");
// Whether to expose the admin / tenant-settings surface. Off by default: one extra entry point
// silently changes the README screenshot of the account menu. Turn it on only to look at it.
const ADMIN = argv.includes("--admin") || process.env.SHOTS_ADMIN === "1";
// --idle: serve the mirror session as idle with no pending question. The README shot wants the
// live question card, but that card locks the composer, so anything that needs the composer
// itself (the skill picker, say) is checked with this on.
const IDLE = argv.includes("--idle") || process.env.SHOTS_IDLE === "1";
// --fleet-lanes N: serve a SYNTHETIC fleet graph of N lanes instead of the hand-written
// nine. The README shots never pass it; it exists because the figure's geometry only goes
// wrong at scale (a real fleet reached 78 lanes, where an arrow from outside the figure
// became a 2,400px line across every row — docs/log/101 §101.13).
const FLEET_LANES = Number(arg("fleet-lanes", "0")) || 0;
// --demo <scenario>: the stateful fleet a demo recording drives (demo.mjs, demo/<scenario>.mjs).
// Launches create sessions and worktrees, and POST /__demo/phase moves the story on. Off, nothing
// changes.
const DEMO_NAME = argv.includes("--demo") ? arg("demo", "day") : "";
if (DEMO_NAME && !/^[a-z]+$/.test(DEMO_NAME)) throw new Error(`--demo: bad scenario name ${DEMO_NAME}`);
const DEMO = DEMO_NAME ? (await import(`./demo/${DEMO_NAME}.mjs`)).fixtures(LOCALE, fx) : null;

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".webp": "image/webp",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".ico": "image/x-icon",
};

// ---- API surface ------------------------------------------------------------------
// Each entry is (query) => JSON body. Keys are exact paths; `re` entries match by
// regexp and receive the captured groups.
const L = (ja, en) => (LOCALE === "ja" ? ja : en);
const exact = {
  "/api/version": () => ({ version: "0.3.0", commit: "demo" }),
  // home_wipe puts the Danger zone tab on the settings rail, so only the workspace guide scenes
  // (SHOT_WS_GUIDE) get it and every other modal shot keeps the rail it was taken with.
  "/api/whoami": () => ({
    ...fx.USER,
    scheduler_enabled: true,
    role: "member",
    ...(process.env.SHOT_WS_GUIDE ? { home_wipe: true } : {}),
  }),
  "/api/tenants": () => ({
    tenants: [{ slug: "demo", name: "Demo Team", role: ADMIN ? "tenant_admin" : "member" }],
    super_admin: false,
  }),
  "/api/workspace": () => ({ state: "running", bootPhase: "" }),
  // A POST is "start a session" (the studio's attach): answer a name so the flow completes.
  "/api/sessions": (q, method) =>
    method === "POST"
      ? { name: "s" + crypto.randomUUID().slice(0, 6) }
      : {
          sessions: FLEET_LANES
            ? fx.sessionsBig(LOCALE, FLEET_LANES)
            : process.env.SHOT_SESSIONS_GUIDE
              ? fx.sessionsBudgetStopped(LOCALE)
              : fx.sessions(LOCALE),
        },
  "/api/sessions/cleanup": () => ({ candidates: fx.cleanupCandidates(LOCALE) }),
  // Past-session search (ADR 0110) for the palette's conversations mode. Hits name sessions from
  // fx.sessions so Enter has something to open, plus one archived session that is not listed.
  "/api/session-search": (q) => fx.sessionSearch(LOCALE, q.get("q") || ""),
  "/api/fleet-graph": () => (FLEET_LANES ? fx.fleetGraphBig(LOCALE, FLEET_LANES) : fx.fleetGraph(LOCALE)),
  "/api/cleanup/archives": () => ({ archives: fx.cleanupArchives(LOCALE) }),
  "/api/repos": () => ({ repos: fx.repos(LOCALE) }),
  // Four connected agents: at five the launch dialog's per-card sub-label starts to
  // clip (real behavior — see the note in this directory's README), which reads as a
  // rendering bug in a screenshot.
  "/api/connections": () => ({
    claude: { connected: true },
    codex: { connected: true },
    cursor: { connected: true },
    copilot: { connected: false },
    kiro: { connected: false },
    agy: { connected: false },
    opencode: { connected: true },
    github: { connected: true },
    bitbucket: { connected: true },
    // Saved Subversion servers (docs/log/41 amendment). A LIST, not a ProviderConn —
    // one entry with an account and one trust-only entry, which is the state a checkout
    // that declined "save credentials" leaves behind.
    svn: [
      { urlPrefix: "https://svn.example.com/design", username: "taro" },
      { urlPrefix: "https://svn.internal.example/legacy", username: "", trustCert: "1" },
    ],
    // The chat and ops cards' connections exist only in a run that asks (SHOT_CHAT / SHOT_OPS),
    // so the scenes taken without them keep their "Not connected" cards.
    ...(process.env.SHOT_CHAT
      ? {
          discord: {
            connected: true, mode: "channel", botName: "Agent Fleet Bot", operator: true, channelId: "100000000000000001",
            threads: true, receive: true, mirrorInput: true, fullText: false, mentionUserId: "100000000000000002",
            events: ["answer-ready", "question", "permission-request", "exit", "session-report"], notify: true,
          },
          slack: { connected: false },
        }
      : {}),
    // The agent pages of the user guide (SHOT_AGENTS_GUIDE): each card in the state its page shows
    // (Claude with account, plan and expiry; the sign-in forms of the unconnected ones; Kiro and
    // Muse Code not yet installed; lcpp on a member's own llama-server). Every other scene keeps the
    // cards it was taken with.
    ...(process.env.SHOT_AGENTS_GUIDE
      ? {
          claude: { connected: true, email: "demo@example.com", plan: "max", days_left: 2 },
          codex: { connected: false },
          cursor: { connected: false },
          copilot: { connected: true },
          kiro: { connected: false, supported: false },
          agy: { connected: false },
          muse: { connected: false, supported: false },
          opencode: { connected: true, envs: ["OPENCODE_API_KEY", "ANTHROPIC_API_KEY"] },
          lcpp: { connected: true, url: "http://192.168.0.10:8080", reachable: true, model: "qwen3-coder-30b-a3b", model_count: 2 },
        }
      : {}),
    ...(process.env.SHOT_OPS
      ? {
          pagerduty: { connected: true },
          grafana: { connected: true, url: "https://grafana.example.com" },
          cloudwatch: { connected: true, profile: "Staging", region: "ap-northeast-1", sso: true },
          aws: { connected: false },
        }
      : {}),
    // The user guide's code pages (SHOT_CODE_GUIDE): GitHub not connected yet, so its card shows
    // the two ways in, and Bitbucket connected with an account and its commit identity.
    ...(process.env.SHOT_CODE_GUIDE
      ? {
          github: { connected: false },
          bitbucket: { connected: true, username: "demo-user", email: "demo@example.com", commitName: "Demo User", commitEmail: "demo@example.com" },
        }
      : {}),
  }),
  "/api/notifications-shaped": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/notifications": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/chat/conversations": () => ({ conversations: fx.conversations(LOCALE) }),
  "/api/assistants": () => fx.assistants(LOCALE),
  // Work items (docs/log/80). Unhandled routes only answer {}, so without this the rail's new
  // surface always renders empty and verifies nothing.
  "/api/work-items": () => fx.workItems(LOCALE),
  // Member inbox (ADR 0111): one request waiting for this member, one they hold, and one
  // they sent, so every card shape of the state table is on screen.
  "/api/inbox": () => inboxFixture(),
  "/api/inbox/settings": () => ({ acceptFrom: "tenant", dnd: false, allowed: [], blocked: [], sealed: true }),
  // A pull request's live read, fetched when its detail panel opens (read.ts WorkItemDetail).
  // Without it the panel shows no CI, conflict or review state — the point of opening it.
  "/api/work-items/detail": (q, method, body) => fx.workItemDetail(LOCALE, body?.key || ""),
  // The state where the tenant has registered an OAuth app (docs/log/80 §80.17). Without it
  // Jira's "connect with OAuth" always renders disabled and the flow cannot be checked.
  "/api/git-oauth": () => ({
    github: { configured: true },
    bitbucket: { configured: true },
    jira: { configured: true },
  }),
  "/api/memos": () => fx.memos(LOCALE),
  "/api/memo-categories": () => fx.memoCategories(LOCALE),
  "/api/schedules": () => fx.schedules(LOCALE),
  // agentMemory on: the memory scene's status header reads it, and nothing else on screen changes
  // with it (the palette gains a mode).
  // imageGeneration only for the user guide's settings shot (SHOT_IMAGE_SETTINGS): with it on, the
  // Agents tab grows the provider order, which no other scene shows.
  // opencodeCatalog "go" only for the agent pages (SHOT_AGENTS_GUIDE): the OpenCode card then shows its
  // billing routes and keys instead of the bare off switch.
  "/api/env/ui-prefs": () => ({
    agentMemory: true,
    ...(process.env.SHOT_IMAGE_SETTINGS ? { imageGeneration: true } : {}),
    ...(process.env.SHOT_AGENTS_GUIDE ? { opencodeCatalog: "go" } : {}),
  }),
  // Agent memory (ADR 0108) for the memory scene: the list, one body, the change log.
  "/api/agents/memory/entries/list": () => fx.memoryList(LOCALE),
  "/api/agents/memory/entries/member-read": (q) => fx.memoryRead(LOCALE, q.get("name") || ""),
  "/api/agents/memory/entries/member-forgotten": () => ({ forgotten: [], projects: [], withheld: 0 }),
  "/api/agents/memory/entries/changes": (q) => fx.memoryChanges(LOCALE, Number(q.get("limit") || 0)),
  "/api/agents/memory/auto-sync": () => ({ phase: "on", want: true, settled: true, nativeMemoryOff: true }),
  "/api/update/status": () => ({ current: "0.3.0", latest: "0.3.0" }),
  "/api/usage": () => ({ agents: fx.usage(LOCALE) }),
  // Cloud cost (docs/log/67). The whole tab is hidden unless profile.available is true, so
  // without this stub the new surface does not exist as far as the harness is concerned.
  "/api/cost/profile": () => fx.costProfile(),
  "/api/cost/me": () => fx.myCloudCost(),
  "/api/admin/cloud-cost": () => fx.adminCloudCost(),
  // Tenant settings -> members -> detail (the entry point appears only with --admin).
  "/api/admin/tenants": () => fx.adminTenants(),
  "/api/admin/workspace-sizing": () => ({
    runtime: "ecs-ec2",
    cpu_effective: false,
    mem_meaning: "slot",
    disk_meaning: "home",
    disk_default_gb: 50,
    // This runtime grows a home in place (ADR 0045's addendum). Without it the disk field
    // in the member detail describes itself as write-once, which is no longer true of the
    // runtime the rest of this stub is imitating.
    disk_grow_only: true,
    slots: [
      { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
      { instance_type: "m7i.xlarge", mem_mib: 16384, vcpu: 4 },
    ],
  }),
  "/api/agents/rtk/gain": () => fx.rtkGain(),
  "/api/stats": () => fx.stats(),
  // Settings > AWS profiles/SSM reads these three as bare arrays (SsmTab.tsx reload()); an object
  // renders "unexpected response" in the tab.
  // Fictional SSM hosts and instances for the user guide's SSM page, served only to a run that
  // asks (SHOT_SSM_HOSTS): every other scene keeps the empty host list it was taken with.
  "/api/ssm/hosts": () =>
    process.env.SHOT_SSM_HOSTS
      ? [
          { id: "h1", alias: "web-01", profileId: "p2", region: "", instanceId: "i-0a1b2c3d4e5f60001", documentName: "", createdAt: "2026-10-01T00:00:00Z" },
          { id: "h2", alias: "batch-01", profileId: "p2", region: "", instanceId: "i-0a1b2c3d4e5f60002", documentName: "", createdAt: "2026-10-01T00:00:00Z" },
          { id: "h3", alias: "prod-web-01", profileId: "p1", region: "", instanceId: "i-0f9e8d7c6b5a40001", documentName: "", createdAt: "2026-10-01T00:00:00Z" },
        ]
      : [],
  "/api/ssm/instances": () => ({
    instances: [
      { instanceId: "i-0a1b2c3d4e5f60001", name: "web-01", computerName: "ip-10-0-1-11.ec2.internal", ipAddress: "10.0.1.11", platformName: "Amazon Linux", pingStatus: "Online" },
      { instanceId: "i-0a1b2c3d4e5f60003", name: "worker-02", computerName: "ip-10-0-2-24.ec2.internal", ipAddress: "10.0.2.24", platformName: "Amazon Linux", pingStatus: "Online" },
      { instanceId: "i-0a1b2c3d4e5f60004", name: "", computerName: "ip-10-0-3-7.ec2.internal", ipAddress: "10.0.3.7", platformName: "Ubuntu", pingStatus: "Online" },
    ],
  }),
  // Settings > Skills (#2792): one kind with every scope group, so the tab renders all its
  // headings (the heading-spacing check walks them, #2813).
  "/api/skills/catalog": () => ({
    kinds: [
      {
        kind: "claude",
        roots: [
          { dir: "/home/dev/.claude/skills", scope: "user", form: "skills", exists: true },
          { dir: "/home/dev/.claude/commands", scope: "user", form: "commands", exists: true },
        ],
        skills: [
          { name: "handoff", description: L("セッションを別のエージェントへ引き継ぐ", "Hand the session over to another agent"), type: "skill", scope: "user", root: "/home/dev/.claude/skills", path: "/home/dev/.claude/skills/handoff/SKILL.md", userInvocable: true, files: ["checklist.md"] },
          { name: "release-notes", description: L("マージ済み PR からリリースノートを下書きする", "Draft release notes from merged pull requests"), argumentHint: "[tag]", type: "skill", scope: "user", root: "/home/dev/.claude/skills", path: "/home/dev/.claude/skills/release-notes/SKILL.md", userInvocable: true },
          { name: "review", description: L("差分を読んで指摘を並べる", "Read the diff and list findings"), type: "skill", scope: "user", root: "/home/dev/.claude/skills", path: "/home/dev/.claude/skills/review/SKILL.md", userInvocable: true },
          // The same name in the commands folder: the skill above wins, so this one is never used.
          { name: "review", description: L("古いレビュー用コマンド", "Older review command"), type: "command", scope: "user", root: "/home/dev/.claude/commands", path: "/home/dev/.claude/commands/review.md", userInvocable: true, shadowedBy: "/home/dev/.claude/skills/review/SKILL.md" },
          { name: "af-build", description: "Build memory and toolchains", type: "skill", scope: "fleet", path: "/home/dev/.claude/skills/af-build/SKILL.md", userInvocable: false },
          { name: "simplify", type: "skill", scope: "cli", userInvocable: true },
        ],
      },
    ],
  }),
  // The body of the opened skill (api/skills/file), keyed by the catalog's own paths so each row
  // shows its own file; any other path is refused, as the real route does outside the roots.
  "/api/skills/file": (q) => {
    const md = (name, desc, body) => `---\nname: ${name}\ndescription: ${desc}\n---\n\n# ${name}\n\n${body}\n`;
    const bodies = {
      "/home/dev/.claude/skills/handoff/SKILL.md": md("handoff", L("セッションを別のエージェントへ引き継ぐ", "Hand the session over to another agent"), L("作業の状況と次の一手をまとめ、引き継ぎ先に渡します。", "Summarise the state of the work and the next step, and pass it on.")),
      "/home/dev/.claude/skills/release-notes/SKILL.md": md("release-notes", L("マージ済み PR からリリースノートを下書きする", "Draft release notes from merged pull requests"), L("前回のタグ以降にマージされた PR を読み、変更点を種類別に並べます。", "Read the pull requests merged since the last tag and group the changes by kind.")),
      "/home/dev/.claude/skills/review/SKILL.md": md("review", L("差分を読んで指摘を並べる", "Read the diff and list findings"), L("変更された差分を読み、バグの可能性が高いものから順に指摘を並べます。", "Read the changed diff and list findings, most likely bug first.")),
      "/home/dev/.claude/commands/review.md": md("review", L("古いレビュー用コマンド", "Older review command"), L("以前使っていたレビュー用のコマンドです。同名のスキルが優先されます。", "The review command used before; the skill of the same name wins.")),
      "/home/dev/.claude/skills/af-build/SKILL.md": md("af-build", "Build memory and toolchains", L("ビルドのメモリとツールチェーンの扱いです。", "How builds use memory and toolchains.")),
    };
    const content = bodies[q.get("path") || ""];
    return content === undefined ? { error: { code: "not_found", message: "no such skill file" } } : { content };
  },
  // Fictional profiles for the user guide's AWS pages (ADR 0131). `name` is the workspace profile
  // name; without it the row's Log in button is disabled (SsmTab.tsx loginOff).
  "/api/ssm/profiles": () => [
    { id: "p1", name: "Production", label: "Production", startUrl: "https://d-1234567890.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "111111111111", roleName: "Deployer", region: "ap-northeast-1", kind: "sso", createdAt: "2026-10-01T00:00:00Z", confirmBeforeUse: true },
    { id: "p2", name: "Staging", label: "Staging", startUrl: "https://d-1234567890.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "222222222222", roleName: "Developer", region: "ap-northeast-1", kind: "sso", createdAt: "2026-10-01T00:00:00Z" },
    { id: "p3", name: "Sandbox", label: "Sandbox", startUrl: "https://d-1234567890.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "333333333333", roleName: "Developer", region: "us-east-1", kind: "sso", createdAt: "2026-10-01T00:00:00Z" },
  ],
  "/api/ssm/sign-ins": () => [],
  // The WS bar's AWS badge and the Settings rows read their login state from here.
  "/api/aws-login/profiles": () => ({
    profiles: [
      { name: "Production", label: "Production", accountId: "111111111111", roleName: "Deployer", state: "none", expiresAt: "", expiring: false },
      { name: "Staging", label: "Staging", accountId: "222222222222", roleName: "Developer", state: "signed_in", expiresAt: "", expiring: false },
      { name: "Sandbox", label: "Sandbox", accountId: "333333333333", roleName: "Developer", state: "renew", expiresAt: "", expiring: false },
    ],
    signIns: [],
    expiring: [],
  }),
  // A pending login request draws a sticky toast over every scene, so it is served only to a run
  // that asks for it (capture.mjs passes SHOT_AWS_LOGIN_REQUEST for the aws-toast scene alone).
  "/api/aws-login": () =>
    process.env.SHOT_AWS_LOGIN_REQUEST
      ? {
          requests: [
            {
              id: "0123456789abcdef01234567", profile: "Production", label: "Production", accountId: "111111111111", roleName: "Deployer",
              waiters: [{ session: "sk4rq2f", command: "npx cdk deploy" }],
              firstAt: new Date().toISOString(), lastAt: new Date().toISOString(), expiresInSec: 840,
            },
          ],
        }
      : { requests: [] },
  // Fictional Google Cloud profiles for the user guide's Google Cloud page. Served only to a run
  // that asks (SHOT_GCP): the WS bar's Google Cloud badge would otherwise appear in every scene.
  // Without it the routes answer {} as an unknown path does, so other scenes stay as they were.
  "/api/gcp/profiles": () =>
    process.env.SHOT_GCP
      ? [
          { id: "g1", label: "prod", name: "prod", loginMethod: "google", project: "acme-shop-prod", quotaProject: "", account: "you@example.com", region: "asia-northeast1", zone: "asia-northeast1-b", impersonateServiceAccount: "deployer@acme-shop-prod.iam.gserviceaccount.com" },
          { id: "g2", label: "staging", name: "staging", loginMethod: "google", project: "acme-shop-staging", quotaProject: "", account: "you@example.com", region: "asia-northeast1", zone: "", impersonateServiceAccount: "" },
          { id: "g3", label: "analytics", name: "analytics", loginMethod: "google", project: "acme-analytics", quotaProject: "acme-billing", account: "", region: "us-central1", zone: "", impersonateServiceAccount: "" },
        ]
      : {},
  "/api/gcp-login/profiles": () =>
    process.env.SHOT_GCP
      ? { profiles: [
          { name: "analytics", label: "analytics", project: "acme-analytics", account: "", state: "none" },
          { name: "prod", label: "prod", project: "acme-shop-prod", account: "you@example.com", state: "none" },
          { name: "staging", label: "staging", project: "acme-shop-staging", account: "you@example.com", state: "signed_in" },
        ] }
      : {},
  // A pending login request draws a sticky toast over every scene (SHOT_GCP_LOGIN_REQUEST, the
  // gcp-toast scene alone).
  "/api/gcp-login": () =>
    process.env.SHOT_GCP && process.env.SHOT_GCP_LOGIN_REQUEST
      ? {
          requests: [
            {
              id: "89abcdef0123456789abcdef", profile: "prod", label: "prod", project: "acme-shop-prod", account: "you@example.com", relogin: false,
              waiters: [{ session: "sk4rq2f", command: "terraform" }],
              firstAt: new Date().toISOString(), lastAt: new Date().toISOString(), expiresInSec: 840,
            },
          ],
        }
      : {},
  // MCP registry (docs/log/48). One row per origin so the settings tab shows all three
  // editability states (user = full CRUD, tenant = disable-only, builtin = code).
  // Secret values arrive masked, exactly as the agent sends them.
  "/api/mcp-servers": () => ({
    servers: [
      {
        id: "b1", name: "pagerduty", label: "PagerDuty", origin: "builtin", transport: "stdio",
        command: "/usr/local/bin/workspace-agent", args: ["mcp-run", "pagerduty"],
        enabled: true, targets: { assistant: true, session: false }, editable: false, ready: true,
      },
      {
        id: "t1", name: "corp-wiki", label: "Corp Wiki", origin: "tenant", transport: "http",
        url: "https://mcp.corp.example.com/mcp", headers: { Authorization: "***" },
        enabled: true, targets: { assistant: true, session: true }, editable: false, ready: true,
      },
      {
        id: "u1", name: "filesystem", origin: "user", transport: "stdio",
        command: "npx", args: ["-y", "@modelcontextprotocol/server-filesystem", "/home/dev/repos"],
        enabled: true, targets: { assistant: false, session: true }, kinds: ["claude", "codex"],
        editable: true, ready: true,
      },
      // The user guide's MCP page also wants a remote server signed in with OAuth, served only
      // to a run that asks (SHOT_MCP_GUIDE) so the scenes taken without it keep their list.
      ...(process.env.SHOT_MCP_GUIDE
        ? [
            {
              id: "u2", name: "tracker", label: "Issue Tracker", origin: "user", transport: "http",
              url: "https://mcp.tracker.example.com/mcp", auth: "oauth",
              enabled: true, targets: { assistant: true, session: true }, editable: true, ready: true,
            },
          ]
        : []),
    ],
    tenantFetchedAt: 1753600000,
  }),
  "/api/mcp-servers/test": () => ({
    ok: true, serverName: "example-mcp", serverVersion: "1.2.0", toolCount: 3,
    tools: ["search", "fetch_page", "list_spaces"], revision: "2026-07-28", elapsedMs: 214,
  }),
  // Image generation (ADR 0081): the widened status and a queue mid-run. Without these the
  // studio pane renders as "no engine available", which shows none of what it is for.
  "/api/imagegen/status": () =>
    process.env.SHOT_IMAGE_SETTINGS ? fx.imagegenStatusLive(LOCALE) : fx.imagegenStatus(LOCALE),
  // The top-bar engine pills (ADR 0084): one row per role, the member shape of
  // console/src/features/engines/wire.ts. A warm chat engine with a model loaded, a
  // countdown and somebody actually using it, plus a stopped image engine — so the pill's
  // two ends (in use / stopped) and the popover's model line all appear. Absent, no pill
  // renders at all.
  "/api/engines/status": () => ({
    engines: [
      {
        key: "llm", api: "chat", state: "running", warm: true,
        warm_model: "qwen3.8-27b-ud-iq4_xs", warm_model_label: "Qwen3.8 27B IQ4_XS",
        stop_eta: new Date(Date.now() + 14 * 60_000).toISOString(), idle_secs: 900,
        queue: { count: 1 },
      },
      { key: "image", api: "images", state: "stopped", idle_secs: 900 },
    ],
  }),
  "/api/imagegen/jobs": () => fx.imagegenJobs(LOCALE),
  // The image studio (ADR 0100). Without these the studio pane cannot open its studio and the
  // guide's pictures show an error instead of the three columns.
  "/api/imagegen/studios": (q, method, body) =>
    method === "POST" ? fx.imagegenNewStudio(crypto.randomUUID(), body) : fx.imagegenStudios(LOCALE),
  "/api/imagegen/history": () => fx.imagegenHistory(),
  "/api/imagegen/knowledge": (q, method) =>
    method === "POST" ? {} : fx.imagegenKnowledge(LOCALE, q.get("scope") || "family", q.get("key") || ""),
  "/api/browser/pages": () => ({ pages: [] }),
  "/api/tts/speakers": () => ({ speakers: [] }),
  "/api/internal-git/repos": () => ({ repos: process.env.SHOT_CODE_GUIDE ? fx.codeGuideInternalRepos() : [] }),
  // The clone dialog's picker for the code pages (SHOT_CODE_GUIDE): Bitbucket's repositories and
  // the branches of the one the scene picks.
  ...(process.env.SHOT_CODE_GUIDE
    ? {
        "/api/connections/git/bitbucket.org/repos": () => ({ repos: fx.codeGuideRemoteRepos() }),
        "/api/connections/git/bitbucket.org/branches": () => fx.codeGuideBranches(),
      }
    : {}),
  "/api/pat": () => ({}),
  "/api/tts/dict": () => ({ entries: [] }),
  "/api/workspace/stats": () => fx.stats(),
  // Preview subdomains (docs/log/81): the issued URLs and the per-workspace settings. This
  // mimics a running Workspace, so the URLs are present; a stopped one has previewUrls empty.
  "/api/env/ws-settings": () => ({
    agentUpdate: false,
    allowAgentUpdate: true,
    previewDomain: "pv.example.com",
    previewPorts: [3000, 8080],
    previewFixedSlug: false,
    previewPublic: false,
    previewCrossOrigin: false,
    previewMaxPorts: 8,
    previewUrls: {
      "3000": "https://k7f2q9x1w3ub5nzt0abc-3000.pv.example.com",
      "8080": "https://k7f2q9x1w3ub5nzt0abc-8080.pv.example.com",
    },
    // The development OIDC IdP block of the preview page (SHOT_PREVIEW_GUIDE): a fictional
    // issuer and client id. Without it the tab shows no IdP, as it always did.
    ...(process.env.SHOT_PREVIEW_GUIDE
      ? {
          devIdpAvailable: true,
          devIdpIssuer: "https://console.example.com/dev-idp",
          devIdpClientId: "af-ws-7d3k9q2m",
        }
      : {}),
  }),
  // Test personas for the same page, served only to a run that asks (SHOT_PREVIEW_GUIDE).
  ...(process.env.SHOT_PREVIEW_GUIDE
    ? {
        "/api/env/ws-settings/dev-idp/personas": () => ({
          max: 20,
          domain: "example.test",
          personas: [
            { id: "p1", handle: "admin", name: "Admin", email: "admin@example.test", sub: "persona:admin" },
            { id: "p2", handle: "viewer", name: "Viewer", email: "viewer@example.test", sub: "persona:viewer" },
          ],
        }),
      }
    : {}),
  // Settings › Agents (the Claude card's behavior) and Settings › Agent instructions for the user
  // guide's agent pages, served only to a run that asks (SHOT_AGENTS_GUIDE).
  ...(process.env.SHOT_AGENTS_GUIDE
    ? {
        "/api/claude/settings": () => fx.agentsGuideClaudeSettings(),
        "/api/user-notes": () => fx.agentsGuideUserNotes(LOCALE),
        "/api/agents/rtk": () => ({ rtk_available: true, codex_rtk: false, copilot_rtk: true, agy_rtk: false, opencode_rtk: true }),
        "/api/connections/kiro/install": () => ({ installed: false }),
        "/api/connections/muse/install": () => ({ installed: false }),
      }
    : {}),
  // Settings › Machine, Toolchains and Databases for the user guide's workspace pages, served only
  // to a run that asks (SHOT_WS_GUIDE): without it these tabs stay as empty as they always were.
  ...(process.env.SHOT_WS_GUIDE
    ? {
        "/api/workspace/machine": () => fx.wsGuideMachine(),
        "/api/env/toolchains": () => fx.wsGuideToolchains(),
        "/api/env/jdk-latest": (q) => fx.wsGuideLatest("jdk", q.get("major") || ""),
        "/api/env/node-latest": (q) => fx.wsGuideLatest("node", q.get("major") || ""),
        "/api/env/databases": () => fx.wsGuideDatabases(),
      }
    : {}),
  // The left rail's Changes view — cross-repo, one call (see FilesChanges).
  "/api/fs/changes": () => fx.fsChanges(LOCALE),
  // The file viewer's git change marks (CodeView LineMarks). None: the fictional files are clean.
  "/api/fs/linemarks": () => ({ added: [], modified: [], deleted: [] }),
};

const re = [
  // The budget dialog of the sessions guide (SHOT_SESSIONS_GUIDE): the spend of the session its
  // budget stopped. Without it the route stays unhandled, as it always was.
  ...(process.env.SHOT_SESSIONS_GUIDE ? [[/^\/api\/sessions\/([^/]+)\/spend$/, (m) => fx.sessionSpendGuide(m[1])]] : []),
  // The move dialog's preview (ADR 0115) for the move scene.
  [/^\/api\/sessions\/([^/]+)\/move\/preview$/, (m) => fx.movePreview(LOCALE, m[1])],
  // The model picker of a target kind: invented ids, so no real third-party model is named.
  [/^\/api\/agents\/(codex|opencode|copilot|kiro)\/models$/, () => ({
    models: [{ id: "demo-coder-large", label: "demo-coder-large" }, { id: "demo-coder-fast", label: "demo-coder-fast" }],
  })],
  [
    /^\/api\/sessions\/([^/]+)\/messages$/,
    (m) => {
      const body = fx.messages(LOCALE, decodeURIComponent(m[1]));
      // SHOT_FORK: user turns carry the anchor a branch point needs, so "Branch here" shows.
      if (process.env.SHOT_FORK) {
        body.messages = body.messages.map((t) => (t.role === "user" ? { ...t, anchorId: `u-${t.idx}` } : t));
      }
      // SHOT_BUSY: a turn is running and nothing waits on the user (the guide's waiting-list shot).
      if (process.env.SHOT_BUSY && body.status === "question") {
        const { pendingQuestions: _q, pendingText: _t, ...rest } = body;
        return { ...rest, status: "working" };
      }
      // SHOT_TRANSLATE: the agent answered in the other language, so the translate button shows.
      if (process.env.SHOT_TRANSLATE && body.status === "question") {
        const other = fx.messages(LOCALE === "ja" ? "en" : "ja", decodeURIComponent(m[1])).messages;
        const { pendingQuestions: _q, pendingText: _t, ...rest } = body;
        return {
          ...rest,
          status: "idle",
          messages: body.messages.map((t, i) => (t.role === "assistant" && other[i] ? other[i] : t)),
        };
      }
      // SHOT_PLAN: the mirror session waits on a plan instead of a question (the guide's plan page).
      if (process.env.SHOT_PLAN && body.status === "question") {
        const { pendingQuestions: _q, pendingText: _t, ...rest } = body;
        return { ...rest, status: "plan", mode: "plan", pendingPlan: fx.pendingPlan(LOCALE) };
      }
      if (!IDLE) return body;
      const { pendingQuestions: _q, pendingText: _t, ...rest } = body;
      return { ...rest, status: body.status === "question" ? "idle" : body.status };
    },
  ],
  // Initialize Git Flow's opening state (ADR 0103 decision 9): develop only on origin, so the
  // dialog shows its origin-only note.
  [/^\/api\/repos\/([^/]+)\/gitflow$/, () => fx.gitflow()],
  // The composer's skill picker (docs/log/50): the session's own skills plus the CLI-bundled
  // tier (source "cli", names only — the shape claude's init frame yields, §9).
  [/^\/api\/sessions\/([^/]+)\/skills$/, () => ({ skills: fx.sessionSkills(LOCALE) })],
  // The "committed" verdict of the changed-files strip (docs/log/68 P2): repo-relative paths
  // that appeared in a commit made since the session started.
  [/^\/api\/sessions\/([^/]+)\/committed$/, () => fx.committedFiles()],
  // The four member-detail routes (docs/log/67 §67.15). cost alone stays off the 4-second
  // stats/sessions polling and is fetched on open and on Apply, because it reads a DB refreshed
  // every 6 hours.
  [/^\/api\/admin\/tenants\/[^/]+\/members$/, () => fx.adminMembers()],
  [/^\/api\/admin\/tenants\/[^/]+\/members\/[^/]+\/stats$/, () => fx.adminMemberStats()],
  [/^\/api\/admin\/tenants\/[^/]+\/members\/[^/]+\/sessions$/, () => fx.adminMemberSessions(LOCALE)],
  [/^\/api\/admin\/tenants\/[^/]+\/members\/[^/]+\/cost$/, () => fx.memberCloudCost()],
  [/^\/api\/repos\/([^/]+)\/graph$/, (m) => fx.graph(LOCALE, decodeURIComponent(m[1]))],
  [/^\/api\/repos\/([^/]+)\/status$/, (m) => fx.scmStatus(LOCALE, decodeURIComponent(m[1]))],
  [/^\/api\/repos\/([^/]+)\/changes$/, (m) => fx.changes(LOCALE, decodeURIComponent(m[1]))],
  [/^\/api\/repos\/([^/]+)\/submodules$/, () => ({ submodules: [] })],
  // What the SVN re-authentication dialog opens with (docs/log/41 amendment): the server
  // this working copy talks to, and the fact that nothing is stored for it yet.
  [
    /^\/api\/repos\/([^/]+)\/svn-auth$/,
    () => ({
      url: "https://svn.example.com/design/trunk",
      urlPrefix: "https://svn.example.com/design",
      username: "",
      hasCred: false,
      trustCert: false,
    }),
  ],
  [/^\/api\/repos\/([^/]+)\/identity$/, () => ({ name: "Demo User", email: "demo@example.com" })],
  [/^\/api\/repos\/([^/]+)\/show$/, (m, q) => fx.show(LOCALE, q.get("sha") || "")],
  [/^\/api\/repos\/([^/]+)\/diff$/, (m, q) => fx.diff(LOCALE, q.get("path") || "")],
  [/^\/api\/chat\/conversations\/([^/]+)$/, (m) => fx.conversation(LOCALE, decodeURIComponent(m[1]))],
  [/^\/api\/usage\/series$/, (m, q) => fx.usageSeries(LOCALE, q)],
  [/^\/api\/fs\/list$/, (m, q) => fx.fsList(LOCALE, q.get("path") || "")],
  [/^\/api\/fs\/tree$/, (m, q) =>
    (process.env.SHOT_GALLERY && fx.galleryTree(q.get("path") || "")) || fx.fsTree(LOCALE, q.get("path") || "")],
  [/^\/api\/fs\/file$/, (m, q) => fx.fsFile(LOCALE, q.get("path") || "")],
  [/^\/api\/imagegen\/props$/, (m, q) => fx.imagegenProps(LOCALE, q.get("path") || "")],
  [/^\/api\/imagegen\/studios\/[^/]+\/draft-log$/, () => fx.imagegenDraftLog()],
  [/^\/api\/imagegen\/studios\/[^/]+\/persona$/, () => fx.imagegenPersona(LOCALE)],
  [/^\/api\/imagegen\/studios\/[^/]+\/press$/, () => ({ version: "v3", jobs: [{ id: "t2", position: 0 }], recorded: true })],
  [/^\/api\/imagegen\/studios\/[^/]+\/(rewind|bind)$/, () => fx.imagegenStudio(LOCALE, studioReads)],
  [
    /^\/api\/imagegen\/studios\/[^/]+$/,
    (m, q, method, body) => {
      if (method === "DELETE") return {};
      const made = fx.imagegenCreatedStudio(m[0].split("/").pop());
      if (made) return method === "PUT" ? { studio: fx.imagegenPatchCreated(made, body) } : made;
      // The member's own edits are kept on top of the fixture, so a PUT is not undone by its
      // own echo half a second later (the pane would snap every field it just changed back).
      if (method === "PUT") {
        for (const [k, v] of Object.entries(body?.draft || {})) studioEdits[k] = v;
        return { studio: withStudioEdits(fx.imagegenStudio(LOCALE, studioReads)) };
      }
      // Each read counts: from the second one on, the agent has made one more edit, so the
      // pane's poll outlines the field it moved (decision 6).
      studioReads++;
      return withStudioEdits(fx.imagegenStudio(LOCALE, studioReads));
    },
  ],
  // Egress allowlist verdicts for the MCP tab (docs/log/48 §9). This deployment HAS the
  // proxy wired and is still log-only, and the corp wiki host is not on the list — the
  // combination that renders the "works today, blocked once enforced" warning.
  // The OAuth row's status (agent: mcpreg.OAuthStatus): connected through a client ID document.
  [
    /^\/api\/mcp-servers\/([^/]+)\/oauth$/,
    (m) => ({
      serverId: m[1], state: "connected", issuer: "https://auth.tracker.example.com", registration: "cimd",
      scopes: ["issues:read", "issues:write"], expiresAt: Math.floor(Date.now() / 1000) + 3600,
      connectedAt: Math.floor(Date.now() / 1000) - 600,
    }),
  ],
  [
    /^\/api\/egress\/check$/,
    (m, q) => ({
      configured: true,
      mode: "log-only",
      enforce: false,
      hosts: Object.fromEntries(
        // The guide's MCP page shows the list, not the allowlist warning (that has its own section).
        q.getAll("host").map((h) => [h, { host: h, allowed: !!process.env.SHOT_MCP_GUIDE || h.endsWith(".anthropic.com"), proposed: false }]),
      ),
    }),
  ],
];

// A generated PNG for the thumbnail route. zlib is in Node, so this is a whole encoder in
// twenty lines: one IHDR, one IDAT of raw scanlines, one IEND. The hue comes from the path,
// so two cards differ and one card is stable between runs.
function swatchPNG(key) {
  const W = 256;
  const H = 256;
  let h = 2166136261;
  for (let i = 0; i < key.length; i++) h = Math.imul(h ^ key.charCodeAt(i), 16777619);
  const base = [64 + (Math.abs(h) % 120), 64 + (Math.abs(h >> 8) % 120), 64 + (Math.abs(h >> 16) % 120)];
  const raw = Buffer.alloc(H * (1 + W * 3));
  for (let y = 0; y < H; y++) {
    const row = y * (1 + W * 3);
    raw[row] = 0; // filter: none
    for (let x = 0; x < W; x++) {
      const o = row + 1 + x * 3;
      const t = (x + y) / (W + H);
      raw[o] = Math.round(base[0] * (0.5 + t * 0.6));
      raw[o + 1] = Math.round(base[1] * (0.5 + t * 0.6));
      raw[o + 2] = Math.round(base[2] * (0.5 + t * 0.6));
    }
  }
  const chunk = (type, body) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(body.length);
    const td = Buffer.concat([Buffer.from(type, "ascii"), body]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td) >>> 0);
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(W, 0);
  ihdr.writeUInt32BE(H, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // colour type: truecolour
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", zlib.deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();

function crc32(buf) {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return c ^ -1;
}

const seenUnknown = new Set();
let studioReads = 0;
const studioEdits = {};
const withStudioEdits = (s) => {
  const draft = { ...s.draft };
  for (const [k, v] of Object.entries(studioEdits)) {
    if (v === null) delete draft[k];
    else draft[k] = v;
  }
  return { ...s, draft };
};

function apiBody(pathname, query, method = "GET", body = null) {
  const demo = DEMO?.route(pathname, query, method, body);
  if (demo !== undefined) return demo;
  if (exact[pathname]) return exact[pathname](query, method, body);
  for (const [rx, fn] of re) {
    const m = rx.exec(pathname);
    if (m) return fn(m, query, method, body);
  }
  if (!seenUnknown.has(pathname)) {
    seenUnknown.add(pathname);
    console.log("[stub] unhandled:", pathname);
  }
  return {};
}

// ---- HTTP -------------------------------------------------------------------------
const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  const p = url.pathname;

  if (p === "/api/events") {
    // Not implemented — a 404 makes the Console fall back to its REST pollers,
    // which is the path this harness feeds.
    res.writeHead(404).end();
    return;
  }
  // Thumbnails. The gallery and the image-generation studio draw `api/fs/download?thumb=`
  // per card, and a JSON body there renders as a broken image in every one of them — so the
  // stub answers a generated PNG whose colour is derived from the path (different cards look
  // different, and the same card is stable between runs).
  if (p === "/api/fs/download") {
    // A fictional file the "files" scene opens is served as itself: the draw.io viewer fetches
    // the diagram from here, and a swatch in its place leaves the pane blank without an error.
    const known = fx.fileBytes(LOCALE, url.searchParams.get("path") || "");
    if (known !== null) {
      res.writeHead(200, { "content-type": "application/octet-stream", "cache-control": "no-store" });
      res.end(known);
      return;
    }
    res.writeHead(200, { "content-type": "image/png", "cache-control": "no-store" });
    res.end(swatchPNG(url.searchParams.get("path") || ""));
    return;
  }
  if (DEMO && p === "/__demo/phase") {
    const phase = url.searchParams.get("phase") || "";
    res.writeHead(200, { "content-type": "application/json; charset=utf-8" });
    res.end(JSON.stringify(DEMO.setPhase(phase)));
    return;
  }
  if (p.startsWith("/api/")) {
    // The request body is read for the handlers that keep what was sent (a new studio's title
    // and draft), so a flow that creates something shows it back instead of a blank.
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      let sent = null;
      try {
        sent = chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : null;
      } catch {}
      // A demo scenario can answer a route as a Server-Sent Events stream (the chat's
      // POST …/stream): a list of { delay, data } frames written in order.
      const frames = DEMO?.stream?.(p, req.method, sent);
      if (frames) return void streamFrames(res, frames);
      // The guide's Skills shot (SHOT_AGENTS_GUIDE) shows Claude's real config folder, which the
      // agent pages name; the fixture keeps ~/.claude for the features-page scene.
      const guideSkills = process.env.SHOT_AGENTS_GUIDE && p.startsWith("/api/skills/");
      const params = new URLSearchParams(url.searchParams);
      if (guideSkills && params.get("path")) params.set("path", params.get("path").replace("/var/lib/af/claude", "/home/dev/.claude"));
      let body = JSON.stringify(apiBody(p, params, req.method, sent));
      if (guideSkills) body = body.replaceAll("/home/dev/.claude", "/var/lib/af/claude");
      res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
      res.end(body);
    });
    return;
  }

  // Static: the real bundle, with SPA fallback to index.html.
  let file = path.join(DIST, p === "/" ? "index.html" : decodeURIComponent(p));
  if (!file.startsWith(DIST)) return void res.writeHead(403).end();
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(DIST, "index.html");
  const buf = fs.readFileSync(file);
  res.writeHead(200, { "content-type": MIME[path.extname(file)] || "application/octet-stream", "cache-control": "no-store" });
  res.end(buf);
});

function inboxFixture() {
  const ja = LOCALE === "ja";
  const ago = (h) => new Date(Date.now() - h * 3600e3).toISOString().replace(/\.\d+Z$/, "Z");
  const person = (k) => ({ userKey: k.replace(/[@.]/g, "-"), email: k, removed: false });
  const base = { recipients: [], assignee: null, assignedToMe: false, canClaim: false, longOpen: false,
    unseen: false, seq: 1, finalAt: "", session: null, pr: null };
  return {
    mailbox: { lastSeq: 7, readSeq: 4, unseen: 2, dnd: false, acceptFrom: "tenant" },
    items: [
      { ...base, id: "i1", kind: "request", state: "open", role: "recipient", delivery: "unread", canClaim: true, unseen: true,
        sender: person("hanako@example.com"), recipients: [{ ...person("taro@example.com"), delivery: "", eligible: false }],
        body: ja ? "main のログインのテストが不安定です。どなたか見てもらえますか？\n昨日から 5 回に 1 回くらい落ちます。" : "Could someone look at the flaky login test on main?\nIt fails about one run in five since yesterday.",
        createdAt: ago(2), updatedAt: ago(2), seq: 7 },
      { ...base, id: "i2", kind: "request", state: "claimed", role: "recipient", delivery: "read", assignedToMe: true,
        assignee: person("taro@example.com"), sender: person("jiro@example.com"),
        body: ja ? "返金 API のタイムアウトを 10 秒から 30 秒に延ばしてください。" : "Please raise the refund API timeout from 10 s to 30 s.", createdAt: ago(30), updatedAt: ago(5), seq: 6,
        session: { name: "sr7fq2m", dir: "/home/dev/repos/payments-api", branch: "fix/refund-timeout", launchedAt: ago(5), by: person("taro@example.com") } },
      { ...base, id: "i3", kind: "dm", state: "", role: "recipient", delivery: "unread", unseen: true,
        sender: person("hanako@example.com"), body: ja ? "昨日のレビュー、ありがとうございました！" : "Thanks for the review yesterday!", createdAt: ago(1), updatedAt: ago(1), seq: 5 },
      { ...base, id: "i4", kind: "request", state: "open", role: "sender", delivery: "read", longOpen: true,
        sender: person("taro@example.com"),
        recipients: [{ ...person("hanako@example.com"), delivery: "declined", eligible: false }, { ...person("jiro@example.com"), delivery: "read", eligible: true }],
        body: ja ? "今週のリリースノート、どなたか担当してもらえますか？" : "Can someone own the release notes this week?", createdAt: ago(24 * 15), updatedAt: ago(48), seq: 3 },
    ],
  };
}

async function streamFrames(res, frames) {
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store" });
  for (const f of frames) {
    await new Promise((r) => setTimeout(r, f.delay || 0));
    res.write(`data: ${JSON.stringify(typeof f.data === "function" ? f.data() : f.data)}\n\n`);
  }
  res.end();
}

// ---- terminal WebSocket -----------------------------------------------------------
// Minimal server-side WebSocket: handshake + unmasked binary frames. The Console's
// xterm attach writes whatever bytes arrive, so replaying a canned screen renders
// exactly like a live PTY. Client frames (input/resize/ping) are ignored.
function wsFrame(payload, opcode = 0x2) {
  const len = payload.length;
  let header;
  if (len < 126) header = Buffer.from([0x80 | opcode, len]);
  else if (len < 65536) {
    header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 126;
    header.writeUInt16BE(len, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 127;
    header.writeBigUInt64BE(BigInt(len), 2);
  }
  return Buffer.concat([header, payload]);
}

server.on("upgrade", (req, socket) => {
  const url = new URL(req.url, "http://localhost");
  if (!url.pathname.endsWith("/ws/terminal")) return void socket.destroy();
  const key = req.headers["sec-websocket-key"] || "";
  const accept = crypto
    .createHash("sha1")
    .update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
    .digest("base64");
  socket.write(
    "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
      `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
  );
  socket.on("error", () => {});
  socket.on("data", () => {}); // drain input/resize/ping frames
  const screen = Buffer.from("\x1b[2J\x1b[H" + fx.ptyScreen(LOCALE), "utf8");
  setTimeout(() => socket.writable && socket.write(wsFrame(screen)), 150);
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`[stub] console+api on http://127.0.0.1:${PORT} (locale=${LOCALE}, dist=${DIST})`);
});
