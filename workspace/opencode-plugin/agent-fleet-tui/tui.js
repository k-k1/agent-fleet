// agent-fleet TUI plugin for an opencode 2 Terminal session on af's shared server (issue #2120).
//
// A session is claimed only while THIS pane is showing it. Every TUI on the server receives every
// session.created, so claiming by event would hand a session to whichever pane saw it first
// (measured: a /new in one pane landed on another pane's sid). The pane that made the session is
// the one that navigates to it.
//
// The Agent creates the pane's first session itself (tagged with af.sid, environment in place
// before the TUI starts). Everything the member does inside the pane afterwards — /new, a session
// picked from the list, a server restart — is this plugin's job, and ONLY for sessions that
// belong to this pane:
//
//   - a root session shown here that carries no af.sid is tagged with this pane's sid, after the
//     pane's environment has been PUT for it;
//   - taking a session is the Agent's decision (`workspace-agent opencode-claim`): it puts the
//     pane's environment the first time the pane shows a session, tagged already or not (a fork
//     carries the tag, not the environment), and lets one pane win when several show the same
//     untagged session; a pane that loses puts nothing, records nothing and claims nothing;
//   - a session tagged with another af sid is never touched, whatever the route says;
//   - the slot file (opencode-sid/<sid>) follows the root session on screen, as the status plugin
//     does for a standalone pane;
//   - the server's environment for a session is memory only, so when the server's pid changes the
//     environment of every session this pane has claimed is sent again.
//
// This runs inside the TUI process, whose environment is the pane's: the same values the shell
// of a --standalone pane sees, minus the server credentials. Without AF_OPENCODE_SHARED=1 (a --standalone
// pane, a TUI started by hand) the plugin does nothing.
import { execFile } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const AGENT_BIN = process.env.AF_WORKSPACE_AGENT_BIN || "/usr/local/bin/workspace-agent";
const CLAIM_TAKEN = 3; // opencode-claim: the session is another slot's
const run = (args) =>
  new Promise((resolve) => {
    try {
      execFile(AGENT_BIN, args, { timeout: 20000 }, (err) => resolve(err ? (typeof err.code === "number" ? err.code : 1) : 0));
    } catch {
      resolve(1);
    }
  });

const ROUTE_POLL_MS = Number(process.env.AF_OPENCODE_TUI_POLL_MS) || 250;
const SERVER_POLL_MS = Number(process.env.AF_OPENCODE_TUI_SERVER_POLL_MS) || 2000;

const shellEnvironment = () => {
  const vars = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (typeof v !== "string" || k === "OPENCODE_SERVER_PASSWORD" || k === "OPENCODE_SERVER_USERNAME") continue;
    vars[k] = v;
  }
  return vars;
};

export default {
  id: "agent-fleet.tui",
  setup(ctx) {
    const sid = process.env.AF_SESSION_SID;
    if (process.env.AF_OPENCODE_SHARED !== "1" || !sid) return;
    const name = process.env.AF_SESSION_NAME;
    // The directory the Agent reads slot -> session ids from (paths.AgentStateDir).
    const sidDir = process.env.HOME ? join(process.env.HOME, ".local/state/agent-fleet/opencode-sid") : null;
    const mine = new Set(); // sessions this pane owns
    const settled = new Map(); // session id -> "mine" | "other" | "child"; a failed read stays unsettled
    const pending = new Map(); // session id -> in-flight claim
    let pid = 0;
    let lastSlot = "";
    let stopped = false;

    const recordSlot = (sessionID) => {
      if (!sidDir) return;
      try {
        mkdirSync(sidDir, { recursive: true });
        writeFileSync(join(sidDir, sid), sessionID);
      } catch {
        /* the Agent also records the slot */
      }
    };

    // The decision is the Agent's (`opencode-claim`): one winner per session however many panes
    // show it, because the tag cannot be compare-and-set here. It puts the pane's environment first
    // and sets the tag second, for a session already tagged with this sid as well (a fork copies
    // the tag, not the environment). A pane that loses changes nothing.
    const claim = async (sessionID) => {
      const info = await ctx.client.session.get({ sessionID });
      if (info.parentID) return "child";
      const md = info.metadata || {};
      if (md["af.managed"] || (md["af.sid"] && md["af.sid"] !== sid)) return "other";
      if (!name) throw new Error("no slot name");
      const code = await run(["opencode-claim", name, sessionID]);
      if (code === CLAIM_TAKEN) return "other";
      if (code !== 0) throw new Error("opencode-claim exited " + code);
      mine.add(sessionID);
      return "mine";
    };

    const consider = (sessionID) => {
      if (stopped || typeof sessionID !== "string" || settled.has(sessionID) || pending.has(sessionID)) return;
      const p = claim(sessionID)
        .then((kind) => {
          if (stopped) return;
          settled.set(sessionID, kind);
          if (kind === "mine") recordSlot(sessionID);
        })
        .catch(() => {
          /* try again on the next poll */
        })
        .finally(() => pending.delete(sessionID));
      pending.set(sessionID, p);
    };

    const showing = () => {
      const route = ctx.ui.router.current();
      return route && route.type === "session" ? route.sessionID : "";
    };

    const routeTick = () => {
      if (stopped) return;
      const shown = showing();
      if (!shown) return;
      consider(shown);
      // The slot follows the session on screen when it is one of ours.
      if (settled.get(shown) === "mine" && shown !== lastSlot) {
        lastSlot = shown;
        recordSlot(shown);
      }
    };

    // One tick at a time: the re-send awaits one `workspace-agent` process per session, which can
    // outlast SERVER_POLL_MS, and a second tick would still see the old pid and send the same
    // environments again.
    let ticking = false;
    const serverTick = async () => {
      if (stopped || ticking) return;
      ticking = true;
      try {
        const info = await ctx.client.server.info();
        if (info && info.pid) {
          if (pid && info.pid !== pid) {
            // A new server process lost every environment: send this pane's again.
            // Through the Agent, which checks the session is still this slot's: a fork the Go side
            // took over from this pane's session carries another tag now.
            for (const sessionID of mine) {
              if ((await run(["opencode-put-env", name, sessionID])) === 0) continue;
              mine.delete(sessionID);
            }
          }
          pid = info.pid;
        }
      } catch {
        /* the server is away; the TUI retries and so do we */
      } finally {
        ticking = false;
      }
    };

    // Created and shown in the same moment is the common case; do not wait for the poll.
    const off = ctx.data.listen((e) => {
      const d = e && e.details;
      if (d && d.type === "session.created" && d.data && !d.data.parentID && d.data.sessionID === showing()) routeTick();
    });
    const routeTimer = setInterval(routeTick, ROUTE_POLL_MS);
    const serverTimer = setInterval(() => void serverTick(), SERVER_POLL_MS);
    routeTick();
    void serverTick();
    return () => {
      stopped = true;
      clearInterval(routeTimer);
      clearInterval(serverTimer);
      off();
    };
  },
};
