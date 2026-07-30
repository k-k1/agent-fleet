// global-setup — L2（e2e/fleet_test.go）の Node 版ハーネス: CP を AUTH=dev で起動し、
// workspace running + shell セッション作成まで整えてからブラウザテストへ渡す。
// 前提（docker / build 済みイメージ / console/dist）が無ければ E2E_CP_BASE を設定せず
// 戻る（テスト側で skip）。E2E_REQUIRE=1 のときは fail に格上げ（CI 用）。
// teardown が使う状態（CP の pid・データ dir 等）は .e2e-state.json に書く。
import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";

const USER = "e2e-ui"; // コンテナ af-ws-e2e-ui / ネットワーク af-net-e2e-ui（L2 と分離）
const ROOT = path.resolve(__dirname, "..");
const STATE = path.join(__dirname, ".e2e-state.json");

function prereqMissing(msg: string): void {
  if (process.env.E2E_REQUIRE === "1") throw new Error(msg);
  console.log(`[ui-e2e] skip: ${msg}`);
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
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
    return prereqMissing("console/dist がありません（npm --prefix console run build）");

  // 前回 run の残骸（teardown まで辿り着けなかったコンテナ / ネットワーク）は
  // 固定名なので名前衝突になり、workspace/start の 120 秒リトライが空回りする。
  // best-effort で先に消しておく（無ければ単に失敗するだけ）。
  spawnSync("docker", ["rm", "-f", `af-ws-${USER}`]);
  spawnSync("docker", ["network", "rm", `af-net-${USER}`]);

  // CP build（e2e/ の buildCP と同じ）。
  const cpBin = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "af-ui-cp-")), "af-cp");
  const build = spawnSync("go", ["build", "-o", cpBin, "."], {
    cwd: path.join(ROOT, "control-plane"),
    encoding: "utf8",
  });
  if (build.status !== 0) throw new Error("build control-plane: " + build.stderr);

  // Workspace データ。ランナー uid ≠ コンテナ dev(uid 1000) 対策で mount 先を 0777 で
  // 先に掘る（詳細は e2e/fleet_test.go の同処理コメント）。
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "af-ui-data-"));
  for (const d of ["home", "claude-config"]) {
    const p = path.join(dataDir, USER, d);
    fs.mkdirSync(p, { recursive: true });
    fs.chmodSync(p, 0o777);
  }

  const cpPort = await freePort();
  const agentPort = await freePort();
  const logDir = path.join(__dirname, "test-results");
  fs.mkdirSync(logDir, { recursive: true });
  const logPath = path.join(logDir, "cp.log"); // 失敗時に artifact として回収される置き場
  const logFd = fs.openSync(logPath, "w");

  const cp = spawn(cpBin, [], {
    env: {
      ...process.env,
      CP_ADDR: `127.0.0.1:${cpPort}`,
      WS_IMAGE: image,
      WS_DATA: dataDir,
      DEV_USER: USER,
      WS_AGENT_PORT: String(agentPort),
      CONSOLE_DIR: dist, // 本物の Console を配信する（ここが L2 との違い）
    },
    stdio: ["ignore", logFd, logFd],
    detached: false,
  });
  const base = `http://127.0.0.1:${cpPort}`;
  fs.writeFileSync(STATE, JSON.stringify({ pid: cp.pid, dataDir, image, user: USER, logPath }));

  await waitFor("CP /healthz", 15_000, async () => (await fetch(`${base}/healthz`)).ok);
  // workspace/start は Agent healthz 待ち（15s）に間に合わないと 500 を返すが冪等 →
  // 200 までリトライしてから running を確認（L2 と同じ）。
  await waitFor("workspace/start accepted", 120_000, async () => {
    const res = await fetch(`${base}/api/workspace/start`, { method: "POST" });
    return res.status === 200;
  });
  await waitFor("workspace running", 60_000, async () => {
    const ws: any = await (await fetch(`${base}/api/workspace`)).json();
    return ws.state === "running";
  });

  // shell セッションを API で用意（LLM クレデンシャル不要）。UI テストは
  // 「一覧に出る → 開ける → 打鍵が届く」に集中する。
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
