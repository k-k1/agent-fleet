// Launch headless Chromium for the screenshot / demo harness without tripping over other sessions.
//
// Sessions share one loopback and one filesystem. A fixed --remote-debugging-port that another
// session already holds does not fail: Chromium silently binds the IPv6 side while
// 127.0.0.1:<port> stays the other session's browser, so the run attaches to someone else's page.
// So: port 0, the port read back from <profile>/DevToolsActivePort, and a profile directory of
// the run's own (a shared one leaks cookies and localStorage between concurrent runs).
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** `--no-sandbox` is an explicit opt-in (CI runners that cannot create the sandbox), never a
 *  default: the Workspace guide forbids adding it on your own. */
export function sandboxArgs({ env = process.env, argv = process.argv.slice(2) } = {}) {
  return env.SHOTS_NO_SANDBOX === "1" || argv.includes("--no-sandbox") ? ["--no-sandbox"] : [];
}

/** Parent of the per-run profile: the session's throwaway dir when there is one. */
export function profileParent(env = process.env) {
  return env.AF_WORK_DIR && fs.existsSync(env.AF_WORK_DIR) ? env.AF_WORK_DIR : os.tmpdir();
}

/** Line 1 of DevToolsActivePort is the port Chromium actually took; 0 while it is not written. */
export function readDevToolsPort(profileDir) {
  try {
    return Number(fs.readFileSync(path.join(profileDir, "DevToolsActivePort"), "utf8").split("\n")[0]) || 0;
  } catch {
    return 0;
  }
}

/** Start Chromium on a free debugging port with its own profile.
 *
 *  Resolves to `{ proc, port, dir, close }`. `close()` kills the browser's process group and
 *  removes the profile; it is safe to call twice and from a synchronous `exit` handler. When the browser never opens
 *  its port the profile is removed and the process killed before the error is thrown, so a failed
 *  start leaves neither a Chromium nor a directory behind.
 *
 *  @param {{ bin?: string, args?: string[], env?: NodeJS.ProcessEnv, argv?: string[],
 *            tries?: number, pauseMs?: number }} [opts] */
export async function launchChromium({
  bin = "/usr/bin/chromium",
  args = [],
  env = process.env,
  argv = process.argv.slice(2),
  tries = 150,
  pauseMs = 100,
} = {}) {
  const dir = fs.mkdtempSync(path.join(profileParent(env), "af-shots-chromium-"));
  let spawnErr = null;
  const proc = spawn(
    bin,
    [
      "--headless=new",
      ...sandboxArgs({ env, argv }),
      "--remote-debugging-address=127.0.0.1",
      "--remote-debugging-port=0",
      `--user-data-dir=${dir}`,
      ...args,
    ],
    // Own process group: Chromium forks zygote / renderer children that outlive a plain kill of
    // the parent (measured: orphans still holding the profile after a crashed run).
    { stdio: ["ignore", "ignore", "ignore"], detached: true },
  );
  proc.on("error", (e) => (spawnErr = e));
  const killGroup = () => {
    try { process.kill(-proc.pid, "SIGKILL"); } catch {}
    try { proc.kill("SIGKILL"); } catch {}
  };
  // Synchronous, for `exit` handlers: kill the whole group and remove the profile.
  const close = () => {
    killGroup();
    try { fs.rmSync(dir, { recursive: true, force: true }); } catch {}
  };
  let port = 0;
  for (let i = 0; i < tries && !port; i++) {
    await sleep(pauseMs);
    if (spawnErr) {
      close();
      throw new Error(`cannot start the browser: ${bin}: ${spawnErr.message}`);
    }
    port = readDevToolsPort(dir);
  }
  if (!port) {
    close();
    throw new Error(`chromium did not open a debugging port (${bin}); if it exits at once in a container without user namespaces, retry with SHOTS_NO_SANDBOX=1`);
  }
  return { proc, port, dir, close };
}
