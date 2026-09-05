// global-teardown — reclaim the CP process, container, network and temporary data best-effort
// (same shape as the teardown in e2e/fleet_test.go). home can contain files owned by the
// container's uid 1000, so when a plain rm fails we run the image itself as root to remove them.
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const STATE = path.join(__dirname, ".e2e-state.json");

export default async function globalTeardown(): Promise<void> {
  if (!fs.existsSync(STATE)) return; // setup skipped
  const st = JSON.parse(fs.readFileSync(STATE, "utf8"));

  if (st.pid) {
    try {
      process.kill(st.pid, "SIGTERM");
      // An unconditional SIGKILL after a fixed sleep can kill an unrelated process when CP dies
      // immediately and its pid is reused. Poll liveness with signal 0 and send no SIGKILL if it
      // is gone before the deadline; force it exactly once if it is not.
      const deadline = Date.now() + 5000;
      for (;;) {
        await new Promise((r) => setTimeout(r, 100));
        try {
          process.kill(st.pid, 0); // still alive
        } catch {
          break; // already exited (ESRCH)
        }
        if (Date.now() >= deadline) {
          process.kill(st.pid, "SIGKILL");
          break;
        }
      }
    } catch {
      /* already gone */
    }
  }
  spawnSync("docker", ["rm", "-f", `af-ws-${st.user}`]);
  spawnSync("docker", ["network", "rm", `af-net-${st.user}`]);
  let cleaned = true;
  try {
    fs.rmSync(st.dataDir, { recursive: true, force: true });
  } catch {
    try {
      spawnSync("docker", [
        "run", "--rm", "--user", "0",
        "-v", `${st.dataDir}:/clean`,
        "--entrypoint", "/bin/sh", st.image,
        "-c", "rm -rf /clean/* /clean/.[!.]* 2>/dev/null || true",
      ]);
      fs.rmSync(st.dataDir, { recursive: true, force: true });
    } catch (err) {
      cleaned = false;
      console.log(`[ui-e2e] teardown: failed to remove dataDir (keeping state): ${err}`);
    }
  }
  // The mkdtemp holding the CP binary (recorded in STATE by setup). It is owned by the runner
  // uid, so a plain rm is enough.
  try {
    if (st.cpBinDir) fs.rmSync(st.cpBinDir, { recursive: true, force: true });
  } catch (err) {
    cleaned = false;
    console.log(`[ui-e2e] teardown: failed to remove cpBinDir (keeping state): ${err}`);
  }
  // Delete STATE only once everything is reclaimed: if a step failed, it still points at what
  // was left behind, and the container/network use fixed names so the next setup's pre-clean
  // picks them up anyway.
  if (cleaned) fs.rmSync(STATE, { force: true });
  console.log(`[ui-e2e] teardown done (CP log: ${st.logPath})`);
}
