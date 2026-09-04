// global-setup — the Node counterpart of the L2 harness (e2e/fleet_test.go): start CP with
// AUTH=dev, bring the workspace to running and create a shell session, then hand over to the
// browser tests. When a prerequisite is missing (docker / a built image / console/dist) it
// returns without setting E2E_CP_BASE so the tests skip; E2E_REQUIRE=1 turns that into a
// failure instead (for CI). State the teardown needs (CP pid, data dir, ...) goes to
// .e2e-state.json.
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";

const USER = "e2e-ui"; // container af-ws-e2e-ui / network af-net-e2e-ui, kept separate from L2
const ROOT = path.resolve(__dirname, "..");
const STATE = path.join(__dirname, ".e2e-state.json");

function prereqMissing(msg: string): void {
  if (process.env.E2E_REQUIRE === "1") throw new Error(msg);
  console.log(`[ui-e2e] skip: ${msg}`);
}

// Reserve `count` free ports at once. Doing listen(0)->close one at a time can collide with
// itself, because a just-closed port can be handed straight back to the next listen(0), so
// hold every listener open, read the numbers, then close them all. The TOCTOU between close
// and CP/Agent actually binding (another process grabbing the port) remains, but CP binds the
// fixed CP_ADDR and has no way to report the real port of a bind on 0. If it happens it shows
// up as the later healthz / running wait timing out, which is acceptable.
async function freePorts(count: number): Promise<number[]> {
  const servers: net.Server[] = [];
  for (let i = 0; i < count; i++) {
    const srv = net.createServer();
    await new Promise<void>((resolve, reject) => {
      srv.on("error", reject);
      srv.listen(0, "127.0.0.1", resolve);
    });
    servers.push(srv);
  }
  const ports = servers.map((srv) => (srv.address() as net.AddressInfo).port);
  await Promise.all(servers.map((srv) => new Promise<void>((r) => srv.close(() => r()))));
  return ports;
}

async function waitFor(desc: string, timeoutMs: number, cond: () => Promise<boolean>): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      if (await cond()) {
        console.log(`[ui-e2e] ok: ${desc}`);
        return;
      }
    } catch {
      /* retry */
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`[ui-e2e] timeout waiting for ${desc}`);
}

export default async function globalSetup(): Promise<void> {
  const image = process.env.WS_IMAGE || "agent-fleet/workspace:dev";

  if (spawnSync("docker", ["--version"]).status !== 0) return prereqMissing("docker not on PATH");
  if (spawnSync("docker", ["image", "inspect", image]).status !== 0)
    return prereqMissing(`workspace image not built: ${image}`);
  const dist = path.join(ROOT, "console", "dist");
  if (!fs.existsSync(path.join(dist, "index.html")))
    return prereqMissing("console/dist is missing (npm --prefix console run build)");

  // Leftovers from a previous run (a container / network whose teardown never ran) use fixed
  // names, so they collide and make the 120 s workspace/start retry spin for nothing. Remove
  // them best-effort first; if they are absent the commands simply fail.
  spawnSync("docker", ["rm", "-f", `af-ws-${USER}`]);
  spawnSync("docker", ["network", "rm", `af-net-${USER}`]);

  // Build CP (same as buildCP in e2e/). The mkdtemp holding it is recorded in STATE so the
  // teardown can remove it; otherwise one directory leaks per run.
  const cpBinDir = fs.mkdtempSync(path.join(os.tmpdir(), "af-ui-cp-"));
  const cpBin = path.join(cpBinDir, "af-cp");
  const build = spawnSync("go", ["build", "-o", cpBin, "."], {
    cwd: path.join(ROOT, "control-plane"),
    encoding: "utf8",
  });
  if (build.status !== 0) {
    fs.rmSync(cpBinDir, { recursive: true, force: true }); // failed before STATE was written
    throw new Error("build control-plane: " + build.stderr);
  }

  // Workspace data. The runner uid differs from the container's dev (uid 1000), so create the
  // mount targets up front with mode 0777 (see the matching comment in e2e/fleet_test.go).
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "af-ui-data-"));
  for (const d of ["home", "claude-config"]) {
    const p = path.join(dataDir, USER, d);
    fs.mkdirSync(p, { recursive: true });
    fs.chmodSync(p, 0o777);
  }

  const [cpPort, agentPort] = await freePorts(2);
  const logDir = path.join(__dirname, "test-results");
  fs.mkdirSync(logDir, { recursive: true });
  const logPath = path.join(logDir, "cp.log"); // collected as an artifact when a test fails
  const logFd = fs.openSync(logPath, "w");

  const cp = spawn(cpBin, [], {
    env: {
      ...process.env,
      CP_ADDR: `127.0.0.1:${cpPort}`,
      WS_IMAGE: image,
      WS_DATA: dataDir,
      DEV_USER: USER,
      WS_AGENT_PORT: String(agentPort),
      CONSOLE_DIR: dist, // serve the real Console — this is what differs from L2
    },
    stdio: ["ignore", logFd, logFd],
    detached: false,
  });
  const base = `http://127.0.0.1:${cpPort}`;
  fs.writeFileSync(STATE, JSON.stringify({ pid: cp.pid, dataDir, cpBinDir, image, user: USER, logPath }));

  await waitFor("CP /healthz", 15_000, async () => (await fetch(`${base}/healthz`)).ok);
  // workspace/start returns 500 when the Agent healthz wait (15 s) does not finish in time, but
  // it is idempotent: retry until 200, then confirm running (same as L2).
  await waitFor("workspace/start accepted", 120_000, async () => {
    const res = await fetch(`${base}/api/workspace/start`, { method: "POST" });
    return res.status === 200;
  });
  await waitFor("workspace running", 60_000, async () => {
    const ws: any = await (await fetch(`${base}/api/workspace`)).json();
    return ws.state === "running";
  });

  // Create the shell session through the API, so no LLM credentials are needed. The UI tests
  // then focus on: it appears in the list -> it opens -> keystrokes reach it.
  const created = await fetch(`${base}/api/sessions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ kind: "shell", title: "e2e-ui" }),
  });
  if (created.status !== 201) throw new Error(`session create: ${created.status} ${await created.text()}`);
  const session: any = await created.json();
  console.log(`[ui-e2e] session created: ${session.name}`);

  process.env.E2E_CP_BASE = base;
  process.env.E2E_SESSION_TITLE = "e2e-ui";
}
