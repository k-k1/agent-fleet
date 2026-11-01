// agent-fleet's shared opencode 2 server (issue #2120): the two things the server's own
// process cannot do for the Terminal sessions that share it.
//
// It does nothing unless af started this server for shared Terminal sessions
// (AF_OPENCODE_SHARED_SERVE=1, no AF_SESSION_SID in its environment). A standalone TUI has a
// server of its own whose environment is already that session's.
//
// 1. Engine token per session. The server's environment carries the workspace-scoped engine token
//    (the provider block reads {env:AF_ENGINE_TOKEN}), so every session would spend as the
//    workspace. A request that carries exactly that token gets the Authorization of the af
//    session it belongs to, minted by the Agent (`workspace-agent opencode-engine-token <name>`).
//    The token is never stored on the session: opencode.db is plaintext and a fork copies
//    metadata. Anything else (another provider, a request without the token) is left alone.
//
// 2. A shell command runs only in a session whose environment is in place. A session's shell
//    environment is held in the server's memory only, and until it is put there the shell sees the
//    SERVER's environment (provider keys, the server password). Several ways lead to a session
//    without one, and none of them is the Agent's restart alone: a server that has just restarted,
//    a session created from inside a TUI with /new, a fork or an older session picked in the TUI
//    (a fork copies metadata but not the environment), and a subagent's session (it inherits
//    metadata and permissions, not the environment). So the shell tool asks the Agent
//    (`workspace-agent opencode-put-env <slot> <session>`) once per session and server generation
//    — this plugin lives in the server process, so its memory is exactly one generation — and the
//    command is REFUSED when the Agent cannot confirm it. The Agent PUTs the pane's environment
//    only onto a session carrying that slot's af.sid and af.name.
//    A Managed session (af.managed) has no pane: its af.managed.name names the af session, and the
//    Agent PUTs this server's own environment plus that session's AF_WORK_DIR and AF_SESSION_NAME
//    (a PUT replaces the whole shell environment, so nothing else may change). The server's
//    environment is the one the helper inherits from this process, never looked up by pid.
//    A new session nobody has tagged yet is waited for (the TUI plugin PUTs and tags it); one that
//    stays untagged for the bound, an old untagged session, and a Managed one made before
//    af.managed.name existed have no pane to take an environment from and run as they always did.
import { execFile } from "node:child_process";

const AGENT_BIN = process.env.AF_WORKSPACE_AGENT_BIN || "/usr/local/bin/workspace-agent";
const TAG_WAIT_MS = Number(process.env.AF_OPENCODE_TAG_WAIT_MS) || 5000;
const NEW_SESSION_MS = 60000; // a session older than this is not waiting for a first tag
const TOKEN_TTL_MS = 10 * 60 * 1000;
const TOKEN_MISS_TTL_MS = 60 * 1000;
const SHELL_TOOLS = new Set(["shell"]);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const shared = () => process.env.AF_OPENCODE_SHARED_SERVE === "1" && !process.env.AF_SESSION_SID;

const putEnvironment = (name, sessionID) =>
  new Promise((resolve) => {
    try {
      execFile(AGENT_BIN, ["opencode-put-env", name, sessionID], { timeout: 20000 }, (err) => resolve(!err));
    } catch {
      resolve(false);
    }
  });

const mintToken = (name) =>
  new Promise((resolve) => {
    try {
      execFile(AGENT_BIN, ["opencode-engine-token", name], { timeout: 20000 }, (err, stdout) => resolve(err ? "" : String(stdout).trim()));
    } catch {
      resolve("");
    }
  });

export default {
  id: "agent-fleet.shared",
  async setup(ctx) {
    if (!shared()) return;
    const get = (sessionID) => ctx.session.get({ sessionID });

    // --- engine token ---
    const tokens = new Map(); // af session name -> { value, until }
    const tokenFor = async (name) => {
      const hit = tokens.get(name);
      if (hit && hit.until > Date.now()) return hit.value;
      const value = await mintToken(name);
      tokens.set(name, { value, until: Date.now() + (value ? TOKEN_TTL_MS : TOKEN_MISS_TTL_MS) });
      return value;
    };
    const names = new Map(); // session id -> af session name
    await ctx.session.hook("http.request", async (event) => {
      const workspaceToken = process.env.AF_ENGINE_TOKEN;
      const headers = event && event.request && event.request.headers;
      if (!workspaceToken || !headers || headers.get("authorization") !== `Bearer ${workspaceToken}`) return;
      let name = names.get(event.sessionID);
      if (!name) {
        try {
          const info = await get(event.sessionID);
          name = info && info.metadata && info.metadata["af.name"];
        } catch {
          return;
        }
        if (typeof name !== "string" || !name) return;
        names.set(event.sessionID, name);
      }
      const token = await tokenFor(name);
      if (token) headers.set("authorization", `Bearer ${token}`);
    });

    // --- shell waits for its environment ---
    const confirmed = new Set(); // sessions whose environment this server generation holds
    const settle = async (sessionID) => {
      const until = Date.now() + TAG_WAIT_MS;
      for (;;) {
        let info;
        try {
          info = await get(sessionID);
        } catch {
          throw new Error("agent-fleet: the session could not be read, so its environment is unknown; the command was not run");
        }
        const md = (info && info.metadata) || {};
        // A Managed session names its af session itself (af.managed.name); one made before that
        // existed has no name and runs as it always did.
        if (md["af.managed"]) return md["af.managed.name"] || undefined;
        if (md["af.sid"] && md["af.name"]) return md["af.name"];
        const created = info && info.time && info.time.created;
        const old = typeof created === "number" && Date.now() - created > NEW_SESSION_MS;
        if (old || info.parentID || Date.now() >= until) return; // no pane to take an environment from
        await sleep(100);
      }
    };
    await ctx.tool.hook("execute.before", async (event) => {
      if (!SHELL_TOOLS.has(String(event && event.tool)) || !event.sessionID || confirmed.has(event.sessionID)) return;
      const name = await settle(event.sessionID);
      if (name) {
        let ok = false;
        for (let i = 0; i < 3 && !ok; i++) {
          ok = await putEnvironment(name, event.sessionID);
          if (!ok) await sleep(300);
        }
        if (!ok) {
          throw new Error("agent-fleet: could not set this session's environment; the command was not run with the server's environment");
        }
      }
      confirmed.add(event.sessionID);
    });
  },
};
