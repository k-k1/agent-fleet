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
// opencode can emit a finalization message.* shortly AFTER session.idle (the
// assistant message settling, usage, etc.). Taken literally that flips the badge
// back to working with no following idle => stuck 進行中. So message.* within a
// short grace window after session.idle is treated as trailing and ignored; a
// genuine new turn always arrives well after that window.
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
  let idleAt = 0;
  const GRACE_MS = 1500; // ignore message.* this long after session.idle (trailing finalize)
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
  // Extract the opencode session id from whichever event carries one, so the recorded
  // slot→session mapping always tracks the CURRENTLY-ACTIVE session — not just the first
  // one created. opencode can start a new session mid-run (e.g. after an interrupt); if
  // we only captured session.created we'd keep resuming/reading the stale session and the
  // chat would show nothing new. session.idle / message.* both carry the live session id.
  const sidOf = (t, props) => {
    if (!props) return null;
    if (props.sessionID) return props.sessionID; // session.*, message.removed, part.*
    if (props.info && props.info.sessionID) return props.info.sessionID; // message.updated
    if (props.part && props.part.sessionID) return props.part.sessionID; // message.part.updated
    return null;
  };
  return {
    event: async ({ event }) => {
      const t = event && event.type;
      if (!t) return;
      const ocid = sidOf(t, event.properties);
      if (ocid) recordOcid(ocid); // keep the slot pinned to the live session
      if (t === "session.idle") {
        idleAt = Date.now();
        set("idle");
      } else if (t.startsWith("message.")) {
        if (Date.now() - idleAt < GRACE_MS) return; // trailing finalize after idle
        set("working");
      }
    },
  };
};
