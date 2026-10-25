// Drives the shipped opencode plugin modules (workspace/opencode-plugin) with fake v1 and v2
// hosts. Run by plugin_modules_test.go; any failed assertion exits non-zero.
//
//   node --experimental-strip-types plugin_modules.mjs <plugin dir> <fake rtk dir> <fake agent log>
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, existsSync, writeFileSync, rmSync, readdirSync, statSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { pathToFileURL } from "node:url";

const [dir, , agentLog] = process.argv.slice(2);
const load = async (f) => (await import(pathToFileURL(join(dir, f)).href)).default;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const until = async (cond, what) => {
  for (let i = 0; i < 100; i++) {
    if (cond()) return;
    await sleep(50);
  }
  assert.fail("timed out waiting for " + what);
};

// A fake opencode 2 context: records the tool hook and exposes an event stream the test feeds.
const v2Host = (sessions = {}) => {
  const hooks = {};
  const queue = [];
  let wake = () => {};
  return {
    hooks,
    push: (...events) => {
      queue.push(...events);
      wake();
    },
    ctx: {
      tool: { hook: async (name, cb) => void (hooks[name] = cb) },
      // Like ctx.session.get: the session's info, a rejection for one that does not exist.
      session: {
        get: async ({ sessionID }) => {
          if (!(sessionID in sessions)) throw new Error("Session.NotFoundError");
          return { id: sessionID, ...sessions[sessionID] };
        },
        hook: async (name, cb) => void (hooks["session." + name] = cb),
      },
      event: {
        subscribe: ({ signal }) => (async function* () {
          while (!signal.aborted) {
            if (queue.length) yield queue.shift();
            else await new Promise((r) => ((wake = r), setTimeout(r, 20)));
          }
        })(),
      },
    },
  };
};

const mods = { caller: await load("agent-fleet-caller.js"), status: await load("agent-fleet-status.js"), rtk: await load("rtk.ts") };

// Shape: one module that both majors accept. v2 reads id + setup, v1 calls server().
const ids = new Set();
for (const [name, m] of Object.entries(mods)) {
  assert.equal(typeof m.id, "string", name + ": v2 needs a stable id");
  assert.equal(typeof m.setup, "function", name + ": v2 needs setup()");
  assert.equal(typeof m.server, "function", name + ": v1 (1.18.29+) needs server()");
  ids.add(m.id);
}
assert.equal(ids.size, 3, "plugin ids must be distinct");

// caller: stamps af's tools only, overwrites a forged value, on both majors.
{
  const afTool = "af_0123abcd_request_browser_action";
  const run = async (stampOn) => {
    const af = { x: "1", _af_caller_sid: "ses_FORGED" };
    await stampOn(afTool, "ses_real", af);
    assert.equal(af._af_caller_sid, "ses_real", "forged value must be overwritten");
    assert.equal(af.x, "1");
    for (const tool of ["other_probe_tool", "af_probe", "af_0123abcd", "shell", ""]) {
      const args = { x: "1" };
      await stampOn(tool, "ses_real", args);
      assert.deepEqual(args, { x: "1" }, tool + " must not be stamped");
    }
    const noSession = { x: "1" };
    await stampOn(afTool, undefined, noSession);
    assert.deepEqual(noSession, { x: "1" });
  };
  const h = v2Host();
  await mods.caller.setup(h.ctx);
  await run(async (tool, sessionID, input) => h.hooks["execute.before"]({ tool, sessionID, input }));
  const v1 = await mods.caller.server();
  await run(async (tool, sessionID, args) => v1["tool.execute.before"]({ tool, sessionID }, { args }));
}

// rtk: `rtk rewrite` exits non-zero when it rewrites (measured), and the plugin must still use it.
{
  const rewritten = async (tool, command, hook) => {
    const args = { command };
    await hook(tool, args);
    return args.command;
  };
  const h = v2Host();
  await mods.rtk.setup(h.ctx);
  assert.ok(h.hooks["execute.before"], "rtk on PATH: the v2 hook must be registered");
  const v2 = (tool, args) => h.hooks["execute.before"]({ tool, input: args });
  assert.equal(await rewritten("shell", "git status", v2), "rtk git status");
  assert.equal(await rewritten("bash", "git status", v2), "rtk git status");
  assert.equal(await rewritten("read", "git status", v2), "git status");
  assert.equal(await rewritten("shell", "unchanged", v2), "unchanged");
  const v1h = await mods.rtk.server();
  const v1 = (tool, args) => v1h["tool.execute.before"]({ tool }, { args });
  assert.equal(await rewritten("bash", "git status", v1), "rtk git status");
  assert.equal(await rewritten("read", "git status", v1), "git status");

  const saved = process.env.PATH;
  process.env.PATH = mkdtempSync(join(tmpdir(), "no-rtk-"));
  try {
    const off = v2Host();
    await mods.rtk.setup(off.ctx);
    assert.equal(off.hooks["execute.before"], undefined, "no rtk: the v2 hook must not be registered");
    assert.deepEqual(await mods.rtk.server(), {}, "no rtk: v1 hooks must be empty");
  } finally {
    process.env.PATH = saved;
  }
}

// status: states reported in order, ids recorded, subagent sessions left out.
{
  const states = () => (existsSync(agentLog) ? readFileSync(agentLog, "utf8").trim().split("\n").filter(Boolean) : []);
  const ocid = () => readFileSync(join(process.env.HOME, ".local/state/agent-fleet/opencode-sid", process.env.AF_SESSION_SID), "utf8");
  const stagedLeft = () => readdirSync(join(process.env.HOME, ".local/state/agent-fleet/opencode-sid/.staging"));
  const ev = (type, data) => ({ type, data });
  const h = v2Host();
  // A record an older plugin left in the old directory: the plugin must neither read, move nor
  // delete it (the Agent arbitrates by age), however many events it records.
  const legacyRec = join(process.env.HOME, ".config/agent-fleet/opencode-sid", process.env.AF_SESSION_SID);
  mkdirSync(dirname(legacyRec), { recursive: true });
  writeFileSync(legacyRec, "ses_stale_from_old_plugin");
  const cleanup = mods.status.setup(h.ctx);
  assert.equal(typeof cleanup, "function", "v2 setup must return its cleanup");
  const settle = async (n, what) => {
    await until(() => states().length >= n, what);
    await sleep(200);
  };
  const line = (s) => `session-status ${s} sid-test`;

  h.push(
    ev("session.created", { sessionID: "ses_parent" }),
    ev("session.created", { sessionID: "ses_child", parentID: "ses_parent" }),
    ev("session.execution.started", { sessionID: "ses_parent" }),
    ev("session.execution.started", { sessionID: "ses_child" }),
    ev("session.execution.succeeded", { sessionID: "ses_child" }),
  );
  await settle(1, "working");
  assert.deepEqual(states(), [line("working")], "a subagent finishing must not flip the badge to idle");
  assert.equal(ocid(), "ses_parent", "the slot must stay pinned to the parent, never a subagent");

  h.push(
    ev("session.execution.succeeded", { sessionID: "ses_parent" }),
    ev("session.execution.started", { sessionID: "ses_parent" }),
    ev("session.execution.interrupted", { sessionID: "ses_parent", reason: "user" }),
    ev("session.execution.started", { sessionID: "ses_parent" }),
    ev("session.execution.failed", { sessionID: "ses_parent" }),
  );
  await settle(6, "five more transitions");
  assert.deepEqual(states(), "working idle working idle working idle".split(" ").map(line));
  cleanup();

  // The stream is live-only: a subagent resumed after a restart never shows its session.created.
  // It must still be recognised (ctx.session.get), and a lookup that fails must change nothing.
  {
    const before = states().length;
    const h2 = v2Host({ ses_root: {}, ses_old_child: { parentID: "ses_root" } });
    const stop = mods.status.setup(h2.ctx);
    h2.push(
      ev("session.execution.started", { sessionID: "ses_root" }),
      ev("session.execution.started", { sessionID: "ses_old_child" }),
      ev("session.execution.succeeded", { sessionID: "ses_old_child" }),
      ev("session.execution.started", { sessionID: "ses_unknown" }),
      ev("session.execution.succeeded", { sessionID: "ses_unknown" }),
    );
    await settle(before + 1, "working");
    assert.deepEqual(states().slice(before), [line("working")], "an unseen subagent must not flip the badge");
    assert.equal(ocid(), "ses_root", "an unseen subagent must not take over the slot");
    stop();
  }

  // Unload while a lookup is in flight: the old bridge must not touch the slot or report once a
  // new setup (a plugin reload) owns it.
  {
    let release;
    const gate = new Promise((r) => (release = r));
    const slow = v2Host();
    slow.ctx.session.get = async () => (await gate, { id: "ses_old" });
    const stopOld = mods.status.setup(slow.ctx);
    slow.push(ev("session.execution.started", { sessionID: "ses_old" }));
    await sleep(100); // the lookup is now pending
    stopOld();
    const fresh = v2Host();
    const stopNew = mods.status.setup(fresh.ctx);
    fresh.push(ev("session.created", { sessionID: "ses_new" }));
    await sleep(100);
    const before = states().length;
    release();
    await sleep(200);
    assert.equal(ocid(), "ses_new", "a stale lookup must not move the slot back");
    assert.equal(states().length, before, "an unloaded plugin must not report");
    stopNew();
  }

  // v1: idle on session.idle, working on message.*, a trailing message.* right after idle ignored.
  const before = states().length;
  const v1 = await mods.status.server();
  await v1.event({ event: { type: "message.updated", properties: { info: { sessionID: "ses_v1" } } } });
  await v1.event({ event: { type: "session.idle", properties: { sessionID: "ses_v1" } } });
  await v1.event({ event: { type: "message.updated", properties: { info: { sessionID: "ses_v1" } } } });
  await until(() => states().length >= before + 2, "two v1 transitions");
  await sleep(200);
  assert.deepEqual(states().slice(before), ["session-status working sid-test", "session-status idle sid-test"]);
  assert.equal(ocid(), "ses_v1");
  assert.equal(readFileSync(legacyRec, "utf8"), "ses_stale_from_old_plugin", "the plugin must leave the old directory alone");
  assert.deepEqual(stagedLeft(), [], "no staged file may be left behind");
  assert.equal(statSync(join(process.env.HOME, ".local/state/agent-fleet/opencode-sid", process.env.AF_SESSION_SID)).mode & 0o777, 0o600);
}
// --- the shared opencode 2 server (issue #2120) -------------------------------------------------

const failPutEnv = process.env.AF_TEST_PUT_ENV_FAIL_FILE;
const sidDirOf = () => join(process.env.HOME, ".local/state/agent-fleet/opencode-sid");
const statusLines = () => (existsSync(agentLog) ? readFileSync(agentLog, "utf8").trim().split("\n").filter((l) => l.startsWith("session-status")) : []);
const shortWait = async () => sleep(150);
const pending = (p) => Promise.race([p.then(() => false), sleep(250).then(() => true)]);
const now = Date.now();

// status on the shared server: no AF_SESSION_SID, so a session reports under the af sid in its
// metadata; each af sid has its own state machine; subagents and untagged sessions are left out.
{
  const saved = process.env.AF_SESSION_SID;
  delete process.env.AF_SESSION_SID;
  try {
    const sessions = {
      ses_a: { metadata: { "af.sid": "sidA" } },
      ses_b: { metadata: { "af.sid": "sidB" } },
      ses_c: { metadata: {} },
      ses_kid: { parentID: "ses_a", metadata: { "af.sid": "sidA" } },
    };
    const before = statusLines().length;
    const h = v2Host(sessions);
    const stop = mods.status.setup(h.ctx);
    const ev = (type, data) => ({ type, data });
    h.push(
      ev("session.execution.started", { sessionID: "ses_a" }),
      ev("session.execution.started", { sessionID: "ses_b" }),
      ev("session.execution.started", { sessionID: "ses_c" }),
      ev("session.execution.started", { sessionID: "ses_kid" }),
      ev("session.execution.succeeded", { sessionID: "ses_kid" }),
    );
    await until(() => statusLines().length >= before + 2, "two working reports");
    await sleep(200);
    assert.deepEqual(statusLines().slice(before).sort(), ["session-status working sidA", "session-status working sidB"], "each session reports under its own af sid; an untagged one and a subagent do not");
    h.push(ev("session.execution.succeeded", { sessionID: "ses_a" }));
    await until(() => statusLines().length >= before + 3, "idle for A");
    await sleep(200);
    assert.deepEqual(statusLines().slice(before + 2), ["session-status idle sidA"], "A going idle must not touch B");
    assert.equal(readFileSync(join(sidDirOf(), "sidA"), "utf8"), "ses_a", "the slot follows the session under the state dir the Agent reads");
    assert.equal(readFileSync(join(sidDirOf(), "sidB"), "utf8"), "ses_b");

    // A session that was untagged when its first event passed is asked about again.
    sessions.ses_c.metadata = { "af.sid": "sidC" };
    h.push(ev("session.execution.started", { sessionID: "ses_c" }));
    await until(() => statusLines().includes("session-status working sidC"), "C reports once tagged");
    stop();
  } finally {
    if (saved !== undefined) process.env.AF_SESSION_SID = saved;
  }
}

// shared server plugin: inert unless af started the server for shared sessions.
{
  const saved = { sid: process.env.AF_SESSION_SID, shared: process.env.AF_OPENCODE_SHARED_SERVE, token: process.env.AF_ENGINE_TOKEN };
  const shared = await load("agent-fleet-shared.js");
  assert.equal(typeof shared.setup, "function");
  assert.deepEqual(await shared.server(), {}, "opencode 1 has no shared server");
  delete process.env.AF_SESSION_SID;
  try {
    delete process.env.AF_OPENCODE_SHARED_SERVE;
    const off = v2Host();
    await shared.setup(off.ctx);
    assert.deepEqual(Object.keys(off.hooks), [], "not af's shared server: no hook may be registered");

    process.env.AF_OPENCODE_SHARED_SERVE = "1";
    process.env.AF_ENGINE_TOKEN = "ws-token";

    // engine token: only a request that carries the workspace token is rewritten, per session.
    {
      const h = v2Host({ ses_one: { metadata: { "af.name": "one" } }, ses_bare: { metadata: {} } });
      await shared.setup(h.ctx);
      const call = async (sessionID, auth) => {
        const headers = new Headers(auth ? { authorization: auth } : {});
        await h.hooks["session.http.request"]({ sessionID, request: { headers } });
        return headers.get("authorization");
      };
      assert.equal(await call("ses_one", "Bearer ws-token"), "Bearer tok-one");
      assert.equal(await call("ses_one", "Bearer someone-elses-key"), "Bearer someone-elses-key", "another provider's key is not ours to replace");
      assert.equal(await call("ses_bare", "Bearer ws-token"), "Bearer ws-token", "a session af has not named keeps the fallback");
      assert.equal(await call("ses_missing", "Bearer ws-token"), "Bearer ws-token");
      assert.equal(await call("ses_one", null), null);
      const asked = readFileSync(agentLog, "utf8").split("\n").filter((l) => l.startsWith("opencode-engine-token"));
      assert.deepEqual(asked, ["opencode-engine-token one"], "the token is minted once per session name, and only for named sessions");
    }

    // the shell runs only once the Agent has confirmed the session's environment on this server
    // generation: tagged sessions (roots, forks, subagents) and new ones alike; failure refuses.
    {
      const sessions = {
        ses_new: { time: { created: Date.now() }, metadata: {} },
        ses_old: { time: { created: now - 3600_000 }, metadata: {} },
        ses_managed: { time: { created: Date.now() }, metadata: { "af.managed": "1" } },
        ses_named: { time: { created: Date.now() }, metadata: { "af.managed": "1", "af.managed.name": "mgdA" } },
        ses_namedkid: { time: { created: Date.now() }, parentID: "ses_named", metadata: { "af.managed": "1", "af.managed.name": "mgdA" } },
        ses_root: { time: { created: now - 3600_000 }, metadata: { "af.sid": "sidR", "af.name": "slotR" } },
        // a subagent session inherits the tag, not the environment; a fork copies the tag too
        ses_kid: { time: { created: Date.now() }, parentID: "ses_root", metadata: { "af.sid": "sidR", "af.name": "slotR" } },
        ses_fork: { time: { created: now - 3600_000 }, metadata: { "af.sid": "sidR", "af.name": "slotR" } },
        ses_oldkid: { time: { created: now - 3600_000 }, parentID: "ses_old", metadata: {} },
      };
      const h = v2Host(sessions);
      await shared.setup(h.ctx);
      const run = (tool, sessionID) => h.hooks["execute.before"]({ tool, sessionID, input: {} });
      const asked = () => readFileSync(agentLog, "utf8").split("\n").filter((l) => l.startsWith("opencode-put-env"));
      const before = asked().length;

      assert.equal(await pending(run("read", "ses_root")), false, "only shell commands are held");
      assert.equal(asked().length, before, "and only they ask the Agent");
      await run("shell", "ses_root");
      await run("bash", "ses_kid");
      await run("shell", "ses_fork");
      assert.deepEqual(asked().slice(before), ["opencode-put-env slotR ses_root", "opencode-put-env slotR ses_kid", "opencode-put-env slotR ses_fork"], "every tagged session — root, subagent, fork — is confirmed by the Agent");
      await run("shell", "ses_root");
      assert.equal(asked().length, before + 3, "confirmed once per session and generation");
      await run("shell", "ses_old");
      await run("shell", "ses_oldkid");
      await run("shell", "ses_managed");
      assert.equal(asked().length, before + 3, "old untagged and Managed sessions have no pane to ask for");
      await run("shell", "ses_named");
      await run("bash", "ses_namedkid");
      assert.deepEqual(asked().slice(before + 3), ["opencode-put-env mgdA ses_named", "opencode-put-env mgdA ses_namedkid"], "a Managed session that names its af session is confirmed under that name, subagents included");

      const waiting = run("shell", "ses_new");
      assert.equal(await pending(waiting), true, "a new untagged session is held until it is claimed");
      sessions.ses_new.metadata = { "af.sid": "sidN", "af.name": "slotN" };
      await waiting;
      assert.equal(asked().pop(), "opencode-put-env slotN ses_new");
    }

    // a session the Agent cannot confirm is refused, not run with the server's environment.
    {
      const sessions = { ses_x: { time: { created: now - 3600_000 }, metadata: { "af.sid": "sidX", "af.name": "slotX" } } };
      const h = v2Host(sessions);
      await shared.setup(h.ctx);
      writeFileSync(failPutEnv, "1");
      try {
        await assert.rejects(h.hooks["execute.before"]({ tool: "shell", sessionID: "ses_x", input: {} }), /could not set this session's environment/);
        await assert.rejects(h.hooks["execute.before"]({ tool: "shell", sessionID: "ses_x", input: {} }), /could not set/, "a refusal is not remembered as success");
      } finally {
        rmSync(failPutEnv, { force: true });
      }
      await h.hooks["execute.before"]({ tool: "shell", sessionID: "ses_x", input: {} });
      await assert.rejects(h.hooks["execute.before"]({ tool: "shell", sessionID: "ses_missing", input: {} }), /could not be read/);
    }

    // a new session nobody claims runs after the bound: there is no pane to take an environment from.
    {
      const h = v2Host({ ses_new: { time: { created: Date.now() }, metadata: {} } });
      await shared.setup(h.ctx);
      const t0 = Date.now();
      await h.hooks["execute.before"]({ tool: "shell", sessionID: "ses_new", input: {} });
      const waited = Date.now() - t0;
      assert.ok(waited >= 600 && waited < 3000, "bounded tag wait (700 ms), got " + waited);
    }
  } finally {
    for (const [k, v] of [["AF_SESSION_SID", saved.sid], ["AF_OPENCODE_SHARED_SERVE", saved.shared], ["AF_ENGINE_TOKEN", saved.token]]) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}

// TUI plugin: claims a session only while its own pane shows it, environment first.
{
  const tui = (await import(pathToFileURL(join(dir, "agent-fleet-tui", "tui.js")).href)).default;
  const saved = { shared: process.env.AF_OPENCODE_SHARED, sid: process.env.AF_SESSION_SID, name: process.env.AF_SESSION_NAME, pw: process.env.OPENCODE_SERVER_PASSWORD };
  process.env.AF_SESSION_SID = "sidP";
  process.env.AF_SESSION_NAME = "paneP";
  process.env.OPENCODE_SERVER_PASSWORD = "secret";
  const world = (sessions, route) => {
    const calls = [];
    const listeners = [];
    const w = {
      calls,
      route,
      pid: 100,
      sessions,
      emit: (type, data) => listeners.forEach((l) => l({ name: type, details: { type, data } })),
      ctx: {
        client: {
          session: {
            get: async ({ sessionID }) => {
              if (!(sessionID in sessions)) throw new Error("404");
              return { id: sessionID, ...sessions[sessionID] };
            },
            update: async ({ sessionID, metadata }) => {
              calls.push(["update", sessionID, metadata]);
              sessions[sessionID].metadata = metadata;
            },
            environment: async ({ sessionID, variables }) => void calls.push(["environment", sessionID, variables]),
          },
          server: { info: async () => ({ pid: w.pid }) },
        },
        ui: { router: { current: () => w.route } },
        data: { listen: (cb) => (listeners.push(cb), () => void listeners.splice(listeners.indexOf(cb), 1)) },
      },
    };
    return w;
  };
  try {
    delete process.env.AF_OPENCODE_SHARED;
    const inert = world({}, { type: "home" });
    assert.equal(await tui.setup(inert.ctx), undefined, "a pane that is not on the shared server does nothing");

    process.env.AF_OPENCODE_SHARED = "1";
    const w = world(
      {
        ses_free: { metadata: { keep: "me" } },
        ses_other: { metadata: { "af.sid": "sidOther" } },
        ses_managed: { metadata: { "af.managed": "1" } },
        ses_kid: { parentID: "ses_free", metadata: {} },
        ses_mine: { metadata: { "af.sid": "sidP" } },
      },
      { type: "home" },
    );
    const stop = tui.setup(w.ctx);
    assert.equal(typeof stop, "function");
    await sleep(120);
    assert.deepEqual(w.calls, [], "nothing is touched while the pane shows no session");

    // another pane's session.created arrives here too: not shown here, not claimed here.
    w.emit("session.created", { sessionID: "ses_free" });
    await sleep(120);
    assert.deepEqual(w.calls, [], "a session this pane does not show is not its to claim");

    for (const id of ["ses_other", "ses_managed", "ses_kid"]) {
      w.route = { type: "session", sessionID: id };
      await sleep(150);
      assert.deepEqual(w.calls, [], id + " must be left alone");
    }

    const claims = () => readFileSync(agentLog, "utf8").split("\n").filter((l) => l.startsWith("opencode-claim"));
    const claimsBefore = claims().length;
    w.route = { type: "session", sessionID: "ses_free" };
    await until(() => claims().length > claimsBefore, "the shown untagged session is claimed through the Agent");
    await sleep(100);
    assert.deepEqual(claims().slice(claimsBefore), ["opencode-claim paneP ses_free"], "the Agent decides, once");
    assert.deepEqual(w.calls, [], "the plugin itself puts nothing and tags nothing: the Agent does both, in order, for one winner");
    assert.equal(readFileSync(join(sidDirOf(), "sidP"), "utf8"), "ses_free");

    // a session another pane won while this one was looking: nothing recorded, nothing claimed.
    w.sessions.ses_lose = { metadata: {} };
    w.route = { type: "session", sessionID: "ses_lose" };
    await until(() => claims().includes("opencode-claim paneP ses_lose"), "the losing claim is attempted");
    await sleep(150);
    assert.equal(readFileSync(join(sidDirOf(), "sidP"), "utf8"), "ses_free", "a lost claim must not move the slot");

    // a session already carrying this pane's tag: still goes through the Agent (a fork copies the
    // tag, not the environment).
    w.route = { type: "session", sessionID: "ses_mine" };
    await until(() => readFileSync(join(sidDirOf(), "sidP"), "utf8") === "ses_mine", "slot follows the shown session");
    assert.ok(claims().includes("opencode-claim paneP ses_mine"));
    assert.deepEqual(w.calls, []);

    // a new server process lost every environment: this pane's sessions get theirs again.
    const putEnvs = () => readFileSync(agentLog, "utf8").split("\n").filter((l) => l.startsWith("opencode-put-env paneP"));
    w.pid = 200;
    await until(() => new Set(putEnvs()).size >= 2, "environments re-sent after a pid change");
    assert.deepEqual(new Set(putEnvs()), new Set(["opencode-put-env paneP ses_free", "opencode-put-env paneP ses_mine"]), "only sessions this pane won are re-sent, through the Agent that checks they are still its own");
    // The fake Agent answers slower than the server poll, so a tick that did not wait for the one
    // before it would send each environment again.
    await sleep(500);
    assert.equal(putEnvs().length, 2, "one pid change re-sends each environment once");
    stop();
  } finally {
    for (const [k, v] of [["AF_OPENCODE_SHARED", saved.shared], ["AF_SESSION_SID", saved.sid], ["AF_SESSION_NAME", saved.name], ["OPENCODE_SERVER_PASSWORD", saved.pw]]) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}
console.log("ok");
