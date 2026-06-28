// agent-fleet status bridge for opencode.
//
// Mirrors what claude does via settings.json hooks: report the session's live
// working/idle state to the Workspace Agent so the Console can badge 進行中 / 入力待ち
// and notify on arrival. opencode has no settings.json hooks, but it does expose a
// plugin event stream — we listen to it and shell out to `workspace-agent
// session-status <state> <sid>`.
//
// The session id we report is OUR deterministic sid (uuidv5(dir|name)), injected as
// AF_SESSION_SID when the Agent launches `opencode --continue` in tmux. opencode's
// own internal session id is irrelevant — one tmux = one opencode = one AF session,
// so all events map to that single sid. The Agent records {state,ts} keyed by it and
// wireSession surfaces it (same store/path as claude). Absent AF_SESSION_SID (e.g. a
// shell-launched opencode) the plugin is a no-op.
//
// State mapping (debounced — only fires on transitions, so ~2 calls per turn):
//   message.* (assistant producing output / user just submitted) -> working
//   session.idle (response finished, awaiting input)             -> idle
//
// It also records the opencode session id (ses_…) on session.created, keyed by
// AF_SESSION_SID, into ~/.config/agent-fleet/opencode-sid/<AF_SESSION_SID>. The Agent
// reads it to resume THIS slot's own session (opencode --session <id>) on relaunch,
// so each slot keeps a distinct conversation instead of sharing via --continue.
import { writeFileSync, mkdirSync } from "fs";
import { join } from "path";

export const AgentFleetStatus = async ({ $ }) => {
  const sid = process.env.AF_SESSION_SID;
  const sidDir = process.env.HOME ? join(process.env.HOME, ".config/agent-fleet/opencode-sid") : null;
  let cur = "";
  const set = (state) => {
    if (!sid || state === cur) return;
    cur = state;
    // Fire-and-forget; absolute path so it resolves regardless of the plugin's PATH.
    try {
      $`/usr/local/bin/workspace-agent session-status ${state} ${sid}`.quiet().catch(() => {});
    } catch {
      /* ignore */
    }
  };
  const recordOcid = (ocid) => {
    if (!sid || !ocid || !sidDir) return;
    try {
      mkdirSync(sidDir, { recursive: true });
      writeFileSync(join(sidDir, sid), ocid);
    } catch {
      /* ignore */
    }
  };
  return {
    event: async ({ event }) => {
      const t = event && event.type;
      if (!t) return;
      if (t === "session.created") recordOcid(event.properties && event.properties.sessionID);
      if (t === "session.idle") set("idle");
      else if (t.startsWith("message.")) set("working");
    },
  };
};
