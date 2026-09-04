// 実ブラウザ検査の土台（scripts/pdf, scripts/doc から使う）。
//
// CP も Agent も要らない検査のために、①一時ディレクトリを素の http で配り
// ②headless Chromium を上げて ③CDP を**素の WebSocket**で叩く、の 3 つだけを持つ。
// puppeteer / playwright は入れない（イメージに焼いた chromium をそのまま使う）。
//
// ポートは必ず 0 で取る: セッションは 1 つのコンテナを共有していて、固定ポートは
// 他のセッションのサーバと衝突する（衝突しても chromium は黙って IPv6 側へ回るので、
// 「別のブラウザに繋がっていた」という形で気づけない）。
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const TYPES = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".pdf": "application/pdf",
  ".wasm": "application/wasm", // これを外すと wasm は instantiateStreaming に落ちる
  ".json": "application/json",
};

/** ディレクトリを 127.0.0.1 の空きポートで配る。
 *
 *  返り値の `requests` は**配ったものの記録**（`{ path, status }` の配列）。
 *  これが要るのは、**「ページが実際に何を取りに来たか」は DOM に出ないから**である
 *  —— 同梱アセット（pdf.js の cMap / 標準14フォント）のコピーを丸ごと落としても、
 *  pdf:check は **11 件すべて OK のまま通っていた**（2026-09-04 実測）。
 *  取りに来た URL と、それを配れたかどうかを見るしかない。 */
export function serveDir(dir) {
  const requests = [];
  const server = http.createServer((req, res) => {
    const urlPath = req.url.split("?")[0];
    const file = path.join(dir, decodeURIComponent(urlPath));
    if (!file.startsWith(dir) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      requests.push({ path: urlPath, status: 404 });
      res.writeHead(404).end("not found");
      return;
    }
    requests.push({ path: urlPath, status: 200 });
    res.writeHead(200, { "Content-Type": TYPES[path.extname(file)] || "application/octet-stream" });
    fs.createReadStream(file).pipe(res);
  });
  return new Promise((r) =>
    server.listen(0, "127.0.0.1", () => r({ server, requests, port: server.address().port })),
  );
}

// 実ブラウザの居場所。**環境ごとに違い、どれも「無いかもしれない」**:
//   - この Workspace のコンテナ … /usr/bin/chromium（イメージに焼いてある）
//   - GitHub の ubuntu-latest  … /usr/bin/chromium も /usr/bin/google-chrome も在る
//     （実測 2026-09-04・ci.yml の使い捨て probe: Chromium 151.0.7922.0 /
//      Google Chrome 151.0.7922.173・CHROME_BIN=/usr/bin/google-chrome）
// 既定を 1 本のパス文字列で持っていると、**在るあいだは正しく、無くなった日に
// 「debugging port が開かない」という原因を指さない形で落ちる**（spawn の ENOENT は
// stdio を捨てているので誰の目にも入らない）。候補を並べて、選んだ実体を名前で言う。
const CHROMIUM_CANDIDATES = ["/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable"];

const usable = (p) => {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
};

/** 使うブラウザの実体を決める。明示された 1 本（引数 / CHROMIUM / CHROME_BIN）は
 *  **黙って別のブラウザへ回さない**（頼んだものと違うもので測るのが一番わかりにくい）。 */
export function resolveChromium(explicit = "") {
  const asked = explicit || process.env.CHROMIUM || "";
  if (asked) {
    if (usable(asked)) return asked;
    throw new Error(`指定されたブラウザが実行できない: ${asked}（CHROMIUM / 引数で指定された）`);
  }
  const tried = [...CHROMIUM_CANDIDATES, process.env.CHROME_BIN || ""].filter(Boolean);
  const found = tried.find(usable);
  if (found) return found;
  throw new Error(
    "実ブラウザが見つからない。CHROMIUM=<path> で指定するか、chromium を入れること。探した先: " + tried.join(" "),
  );
}

/** headless Chromium を上げ、CDP を繋いだハンドルを返す。 */
export async function startBrowser({ chromium = "", size = "1000,760" } = {}) {
  const bin = resolveChromium(chromium);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-headless-"));
  const proc = spawn(bin, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
    "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
    `--user-data-dir=${dir}`, `--window-size=${size}`,
    // headless は既定で「ホバーもポインタも無い」と答える。デスクトップの見た目を
    // 検査するときはこれが要る（workspace-notes「Headless browser」）。
    "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4",
    "about:blank",
  ], { stdio: ["ignore", "ignore", "ignore"] });

  // spawn の失敗（ENOENT / EACCES）は既定では誰にも見えない —— stdio を捨てているので
  // 12 秒待って「debugging port が開かない」に化ける。理由をそのまま持って落ちる。
  let spawnErr = null;
  proc.on("error", (e) => (spawnErr = e));

  let port = 0;
  for (let i = 0; i < 120 && !port; i++) {
    await sleep(100);
    if (spawnErr) throw new Error(`ブラウザを起動できない: ${bin}: ${spawnErr.message}`);
    try {
      port = Number(fs.readFileSync(path.join(dir, "DevToolsActivePort"), "utf8").split("\n")[0]) || 0;
    } catch {}
  }
  if (!port) throw new Error(`chromium did not open a debugging port (${bin})`);

  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const ws = new WebSocket(targets.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  const logs = [];
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m);
      pending.delete(m.id);
    }
    if (m.method === "Runtime.exceptionThrown") {
      logs.push("exception: " + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
    }
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const i = ++id;
      pending.set(i, (m) => (m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m.result)));
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  const evaluate = async (expression) => {
    const r = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "evaluate failed");
    return r.result.value;
  };
  await send("Page.enable");
  await send("Runtime.enable");
  return {
    send,
    evaluate,
    logs,
    goto: (url) => send("Page.navigate", { url }),
    screenshot: async (out) => {
      const shot = await send("Page.captureScreenshot", { format: "png" });
      fs.writeFileSync(out, Buffer.from(shot.data, "base64"));
    },
    close: () => {
      ws.close();
      proc.kill();
      try {
        fs.rmSync(dir, { recursive: true, force: true });
      } catch {}
    },
  };
}

/** 条件が成立するまでポーリングし、最後に読んだ値を返す（成立しなくても返す）。 */
export async function until(evaluate, expression, want, tries = 100, waitMs = 100) {
  let last;
  for (let i = 0; i < tries; i++) {
    last = await evaluate(expression);
    if (want(last)) return last;
    await sleep(waitMs);
  }
  return last;
}

/** 結果の集計。check(条件, 名前, 補足) で足し、report() で出して終了コードを決める。 */
export function checker() {
  const ok = [];
  const bad = [];
  return {
    check: (cond, label, detail = "") => (cond ? ok : bad).push(label + (detail ? ` — ${detail}` : "")),
    report: () => {
      for (const line of ok) console.log("  OK   " + line);
      for (const line of bad) console.log("  NG   " + line);
      console.log(bad.length ? `\n${bad.length} 件が NG` : `\n${ok.length} 件すべて OK`);
      return bad.length ? 1 : 0;
    },
  };
}
