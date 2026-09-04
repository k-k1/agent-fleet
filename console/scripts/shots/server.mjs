// Stub Control Plane for the README screenshot harness.
//
// Serves the real console bundle (console/dist) plus just enough of the CP's API
// surface, answered from fixtures.mjs, for the Console to render a populated fleet
// with no backend, no Docker and no real data. Unknown /api paths answer {} and are
// logged, so a missing endpoint shows up as a log line instead of a hung view.
//
//   node console/scripts/shots/server.mjs [--port 8765] [--locale ja]
import http from "node:http";
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
const exact = {
  "/api/version": () => ({ version: "0.3.0", commit: "demo" }),
  "/api/whoami": () => ({ ...fx.USER, scheduler_enabled: true, role: "member" }),
  "/api/tenants": () => ({
    tenants: [{ slug: "demo", name: "Demo Team", role: ADMIN ? "tenant_admin" : "member" }],
    super_admin: false,
  }),
  "/api/workspace": () => ({ state: "running", bootPhase: "" }),
  "/api/sessions": () => ({ sessions: fx.sessions(LOCALE) }),
  "/api/sessions/cleanup": () => ({ candidates: fx.cleanupCandidates(LOCALE) }),
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
  }),
  "/api/notifications-shaped": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/notifications": () => ({ items: [], maxSeq: 0, unseenCount: 0, sourceState: "ready" }),
  "/api/chat/conversations": () => ({ conversations: fx.conversations(LOCALE) }),
  "/api/assistants": () => fx.assistants(LOCALE),
  // Work items (docs/log/80). Unhandled routes only answer {}, so without this the rail's new
  // surface always renders empty and verifies nothing.
  "/api/work-items": () => fx.workItems(LOCALE),
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
  "/api/env/ui-prefs": () => ({}),
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
    slots: [
      { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
      { instance_type: "m7i.xlarge", mem_mib: 16384, vcpu: 4 },
    ],
  }),
  "/api/agents/rtk/gain": () => fx.rtkGain(),
  "/api/stats": () => fx.stats(),
  "/api/ssm/hosts": () => ({ hosts: [] }),
  "/api/ssm/profiles": () => ({ profiles: [] }),
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
    ],
    tenantFetchedAt: 1753600000,
  }),
  "/api/mcp-servers/test": () => ({
    ok: true, serverName: "example-mcp", serverVersion: "1.2.0", toolCount: 3,
    tools: ["search", "fetch_page", "list_spaces"], revision: "2026-07-28", elapsedMs: 214,
  }),
  "/api/browser/pages": () => ({ pages: [] }),
  "/api/tts/speakers": () => ({ speakers: [] }),
  "/api/internal-git/repos": () => ({ repos: [] }),
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
  }),
  // The left rail's Changes view — cross-repo, one call (see FilesChanges).
  "/api/fs/changes": () => fx.fsChanges(LOCALE),
};

const re = [
  [/^\/api\/sessions\/([^/]+)\/messages$/, (m) => fx.messages(LOCALE, decodeURIComponent(m[1]))],
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
  [/^\/api\/repos\/([^/]+)\/identity$/, () => ({ name: "Demo User", email: "demo@example.com" })],
  [/^\/api\/repos\/([^/]+)\/show$/, (m, q) => fx.show(LOCALE, q.get("sha") || "")],
  [/^\/api\/repos\/([^/]+)\/diff$/, (m, q) => fx.diff(LOCALE, q.get("path") || "")],
  [/^\/api\/chat\/conversations\/([^/]+)$/, (m) => fx.conversation(LOCALE, decodeURIComponent(m[1]))],
  [/^\/api\/usage\/series$/, (m, q) => fx.usageSeries(LOCALE, q)],
  [/^\/api\/fs\/list$/, (m, q) => fx.fsList(LOCALE, q.get("path") || "")],
  [/^\/api\/fs\/tree$/, (m, q) => fx.fsTree(LOCALE, q.get("path") || "")],
  [/^\/api\/fs\/file$/, (m, q) => fx.fsFile(LOCALE, q.get("path") || "")],
  // Egress allowlist verdicts for the MCP tab (docs/log/48 §9). This deployment HAS the
  // proxy wired and is still log-only, and the corp wiki host is not on the list — the
  // combination that renders the "works today, blocked once enforced" warning.
  [
    /^\/api\/egress\/check$/,
    (m, q) => ({
      configured: true,
      mode: "log-only",
      enforce: false,
      hosts: Object.fromEntries(
        q.getAll("host").map((h) => [h, { host: h, allowed: h.endsWith(".anthropic.com"), proposed: false }]),
      ),
    }),
  ],
];

const seenUnknown = new Set();

function apiBody(pathname, query) {
  if (exact[pathname]) return exact[pathname](query);
  for (const [rx, fn] of re) {
    const m = rx.exec(pathname);
    if (m) return fn(m, query);
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
  if (p.startsWith("/api/")) {
    const body = JSON.stringify(apiBody(p, url.searchParams));
    res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
    res.end(body);
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
