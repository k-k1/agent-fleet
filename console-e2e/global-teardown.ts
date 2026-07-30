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
      // 固定スリープ後の無条件 SIGKILL は、CP がすぐ死んで pid が再利用された場合に
      // 無関係プロセスを殺しうる。signal 0 で生存をポーリングし、期限内に消えたら
      // SIGKILL を送らない（消えなければ最後に一度だけ強制終了）。
      const deadline = Date.now() + 5000;
      for (;;) {
        await new Promise((r) => setTimeout(r, 100));
        try {
          process.kill(st.pid, 0); // まだ生きている
        } catch {
          break; // 終了済み（ESRCH）
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
      console.log(`[ui-e2e] teardown: dataDir の回収に失敗（state は残す）: ${err}`);
    }
  }
  // CP バイナリの mkdtemp 置き場（setup が STATE に記録）。runner uid 所有なので素の rm でよい。
  try {
    if (st.cpBinDir) fs.rmSync(st.cpBinDir, { recursive: true, force: true });
  } catch (err) {
    cleaned = false;
    console.log(`[ui-e2e] teardown: cpBinDir の回収に失敗（state は残す）: ${err}`);
  }
  // STATE は全回収が済んでから消す — 途中で失敗しても残骸の手がかりが残り、
  // コンテナ/ネットワークは固定名なので次回 setup の事前掃除でも回収される。
  if (cleaned) fs.rmSync(STATE, { force: true });
  console.log(`[ui-e2e] teardown done (CP log: ${st.logPath})`);
}
