// global-teardown — CP プロセス・コンテナ・ネットワーク・一時データを best-effort で
// 回収する（e2e/fleet_test.go の teardown と同型。home はコンテナ内 uid 1000 の所有物を
// 含みうるため、消せなければイメージ自身を root で回して rm）。
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const STATE = path.join(__dirname, ".e2e-state.json");

export default async function globalTeardown(): Promise<void> {
  if (!fs.existsSync(STATE)) return; // setup が skip した
  const st = JSON.parse(fs.readFileSync(STATE, "utf8"));

  if (st.pid) {
    try {
      process.kill(st.pid, "SIGTERM");
    } catch {
      /* already gone */
    }
    await new Promise((r) => setTimeout(r, 2000));
    try {
      process.kill(st.pid, "SIGKILL");
    } catch {
      /* exited cleanly */
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
      console.log(`[ui-e2e] teardown: dataDir の回収に失敗（state は残す）: ${err}`);
    }
  }
  // STATE は全回収が済んでから消す — 途中で失敗しても残骸の手がかりが残り、
  // コンテナ/ネットワークは固定名なので次回 setup の事前掃除でも回収される。
  if (cleaned) fs.rmSync(STATE, { force: true });
  console.log(`[ui-e2e] teardown done (CP log: ${st.logPath})`);
}
