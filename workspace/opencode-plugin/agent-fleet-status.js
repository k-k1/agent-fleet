// agent-fleet status bridge for opencode.
//
// Mirrors what claude does via settings.json hooks: report the session's live
// working/idle state to the Workspace Agent so the Console can badge 進行中 / 入力待ち
// and notify on arrival. opencode has no settings.json hooks, but it does expose a
// plugin event stream — we listen to it and shell out to `workspace-agent
// session-status <state> <sid>`.
//
// The session id we report is OUR deterministic sid (uuidv5(dir|name)). A --standalone pane gets
// it as AF_SESSION_SID: one tmux = one opencode = one AF session, so all its events map to that
// single sid. The Agent records {state,ts} keyed by it and wireSession surfaces it (same
// store/path as claude).
//
// Shared serve (opencode 2, issue #2120): af runs ONE `opencode serve` for every Terminal session, so
// the server's environment has no AF_SESSION_SID and sessions are told apart by metadata instead:
// af stamps {"af.sid"} on every opencode session it creates (and the TUI plugin does for the ones
// made with /new), and the sid of an event's session comes from ctx.session.get. Each af sid has
// its own debounced state machine. A subagent's session inherits the metadata but is left out by
// its parentID, exactly as before. A session with no af.sid (Managed ones, a member's own `opencode
// run`) is not ours to report. With AF_SESSION_SID in the environment (a --standalone pane, which
// runs its own server) the single env sid applies to every session.
//
// No dependency on @opencode/plugin: opencode 2 reads `id` and `setup` from a plain
// default-exported object.
//
// State mapping (debounced — only fires on transitions, so ~2 calls per turn):
//   session.execution.started                                         -> working
//   session.execution.succeeded | failed | interrupted | session.idle -> idle
//   (measured on 2.0.28: started/succeeded bracket a turn; the others by their names)
//
// It also records the opencode session id (ses_…) of the live session, keyed by
// AF_SESSION_SID, into ~/.local/state/agent-fleet/opencode-sid/<AF_SESSION_SID>. The Agent
// reads it to resume THIS slot's own session (opencode --session <id>) on relaunch,
// so each slot keeps a distinct conversation instead of sharing via --continue.
import { execFile } from "node:child_process";
import { writeFileSync, mkdirSync, renameSync, unlinkSync } from "node:fs";
import { join } from "node:path";

// The override exists for tests (plugin_modules_test.go): they must observe the reported states
// without touching a real Agent.
const AGENT_BIN = process.env.AF_WORKSPACE_AGENT_BIN || "/usr/local/bin/workspace-agent";
// How long an untagged session's turn waits for its tag; the shared-server plugin waits as long
// before a shell command in such a session.
const TAG_WAIT_MS = Number(process.env.AF_OPENCODE_TAG_WAIT_MS) || 5000;
const TAG_POLL_MS = 100;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const bridge = () => {
  const envSid = process.env.AF_SESSION_SID;
  // Where the Agent reads slot -> session ids (paths.AgentStateDir/opencode-sid). It does not
  // follow XDG_STATE_HOME, so neither does this.
  const sidDir = process.env.HOME ? join(process.env.HOME, ".local/state/agent-fleet/opencode-sid") : null;
  let queue = Promise.resolve();
  // Set when the plugin unloads. Whatever is still in flight (a session lookup, a queued report)
  // must not write the slot or report a state after a reload's new bridge took over.
  let stopped = false;
  // One debounced state machine per af session: the shared serve hosts several at once.
  const machines = new Map();
  const machine = (sid) => {
    if (!machines.has(sid)) machines.set(sid, { cur: "" });
    return machines.get(sid);
  };
  const set = (sid, state) => {
    if (!sid || stopped) return;
    const m = machine(sid);
    if (state === m.cur) return;
    m.cur = state;
    // Fire-and-forget, but one at a time: separate processes finish in any order, and a
    // working that lands after its idle leaves the badge stuck. Absolute path so it resolves
    // regardless of the plugin's PATH.
    queue = queue.then(
      () =>
        new Promise((resolve) => {
          if (stopped) return resolve();
          try {
            execFile(AGENT_BIN, ["session-status", state, sid], () => resolve());
          } catch {
            resolve();
          }
        }),
    );
  };
  const recordOcid = (sid, ocid) => {
    if (!sid || !ocid || !sidDir || stopped) return;
    // Staged and renamed (as the Agent's own stores do): the Agent reads this file from another
    // process, and a truncate-then-write would let it see an empty or partial id.
    const staging = join(sidDir, ".staging");
    const tmp = join(staging, `${sid}.${process.pid}.${Date.now()}`);
    try {
      mkdirSync(staging, { recursive: true });
      writeFileSync(tmp, ocid, { mode: 0o600 });
      renameSync(tmp, join(sidDir, sid));
    } catch {
      try {
        unlinkSync(tmp);
      } catch {
        /* nothing staged */
      }
    }
  };
  const idle = (sid) => set(sid, "idle");

  // The payload is under `data`. A subagent's own execution would otherwise flip
  // the badge to idle while the parent still works, and pin the slot to the subagent's id, so
  // child sessions are left out of both. The event stream is live-only: a subagent resumed after
  // a restart or reload never shows its session.created here, so an unseen id is looked up
  // (`lookup` = ctx.session.get). A failed lookup leaves the state and the id alone.
  //
  // owner(id) is the af sid a session reports under: undefined = not known yet or not ours (a
  // failed lookup, a session af has not tagged yet — the next event asks again), null = a
  // subagent or Managed session. A tagged root session is remembered for good; a tag does not
  // change, and a Managed session (af.managed) is never given an af.sid.
  const known = new Map(); // session id -> af sid | null (subagent, Managed)
  const remember = (id, sid) => (known.set(id, sid), sid);
  const owner = async (id, lookup, created) => {
    if (known.has(id)) return known.get(id);
    if (created) {
      // session.created tells it is a subagent without a lookup; the metadata still needs one.
      if (created.parentID) return remember(id, null);
      if (envSid) return remember(id, envSid);
    }
    try {
      const info = await lookup({ sessionID: id });
      if (info && info.parentID) return remember(id, null);
      const md = (info && info.metadata) || {};
      if (!envSid && md["af.managed"]) return remember(id, null);
      const sid = envSid || md["af.sid"];
      if (typeof sid !== "string" || !sid) return undefined;
      return remember(id, sid);
    } catch {
      return undefined;
    }
  };

  // A session made with /new is tagged by the pane's TUI plugin only once the pane shows it, and
  // opencode creates it on the first submit, so its turn starts before it has an af.sid
  // (measured on 2.1.4 and 2.2.0: session.created, then execution.started 160-250 ms later, both
  // untagged; tagged by execution.succeeded). Dropping that start loses the turn's working report,
  // and its idle is then a repeat of the previous turn's and is debounced away, so the badge never
  // moves. So the turn of a session created untagged moments ago is held, the session asked about
  // again until TAG_WAIT_MS after its creation, and the turn replayed (working, then idle if it has
  // already ended) once the tag appears.
  //
  // Only a fresh session is held: an older untagged session that a pane claims later (picked from
  // the list) ran its turns before it was anyone's, and replaying them would report a stale turn
  // and point the slot back at it. While a turn is held, events only update it and the poller alone
  // resolves the owner: an event's own lookup could return an untagged answer after the poller
  // has replayed, and drop the turn's end.
  const fresh = new Map(); // untagged root session id -> creation time (ms) seen here
  const unowned = new Map(); // session id -> { done } of a held turn
  const replay = (id, sid) => {
    const turn = unowned.get(id);
    if (!turn) return;
    unowned.delete(id);
    // No recordOcid: on the shared server the slot is the TUI plugin's (it records the session it
    // claims and follows the screen), and a replay is late — the pane may have moved on since.
    set(sid, "working");
    if (turn.done) idle(sid);
  };
  const awaitTag = async (id, turn, until, lookup) => {
    while (Date.now() < until) {
      await sleep(Math.min(TAG_POLL_MS, Math.max(0, until - Date.now())));
      if (stopped || unowned.get(id) !== turn) return;
      // A lookup that hangs must not hold the turn past the wait.
      const sid = await Promise.race([owner(id, lookup), sleep(Math.max(0, until - Date.now())).then(() => undefined)]);
      if (stopped || unowned.get(id) !== turn) return;
      if (sid === null) return void unowned.delete(id);
      if (typeof sid === "string") return replay(id, sid);
    }
    if (unowned.get(id) === turn) unowned.delete(id); // never tagged in time: not a pane's session, as before
  };
  const onEvent = async (event, lookup) => {
    const t = event && event.type;
    const data = event && event.data;
    if (!t || !data || typeof data.sessionID !== "string") return;
    const id = data.sessionID;
    if (t === "session.created") {
      const sid = await owner(id, lookup, data);
      if (stopped) return;
      if (typeof sid === "string") recordOcid(sid, id);
      else if (sid === undefined) {
        const now = Date.now();
        for (const [k, at] of fresh) if (now - at >= TAG_WAIT_MS) fresh.delete(k);
        fresh.set(id, now);
      }
      return;
    }
    const working = t === "session.execution.started";
    const done =
      t === "session.execution.succeeded" ||
      t === "session.execution.failed" ||
      t === "session.execution.interrupted" ||
      t === "session.idle";
    if (!working && !done) return;
    const held = unowned.get(id);
    if (held && !known.has(id)) {
      held.done = done;
      return;
    }
    const sid = await owner(id, lookup);
    if (stopped) return;
    if (sid === undefined) {
      const at = fresh.get(id);
      fresh.delete(id);
      const until = at === undefined ? 0 : at + TAG_WAIT_MS;
      if (working && Date.now() < until && !unowned.has(id)) {
        const turn = { done: false };
        unowned.set(id, turn);
        void awaitTag(id, turn, until, lookup);
      }
      return;
    }
    fresh.delete(id);
    if (sid === null) return void unowned.delete(id);
    replay(id, sid);
    if (working) {
      recordOcid(sid, id);
      set(sid, "working");
    } else {
      idle(sid);
    }
  };
  return { onEvent, stop: () => void (stopped = true) };
};

export default {
  id: "agent-fleet.status",
  // The subscription is an async iterator; the returned cleanup aborts it.
  setup(ctx) {
    const { onEvent, stop } = bridge();
    const ac = new AbortController();
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: ac.signal })) await onEvent(event, (input) => ctx.session.get(input));
      } catch {
        /* aborted on unload, or the stream ended */
      }
    })();
    return () => {
      stop();
      ac.abort();
    };
  },
};
