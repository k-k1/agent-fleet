// `.drawio` ペインの描画ハーネス（docs/65）。
//
// 守っているもの:
//  1. **同梱ビューアだけで図が出る**（外部ネットワーク 0 件）。ビューアは既定値として
//     viewer.diagrams.net を各所に持つので、1 本でも出ていたら「オフラインで動く」は嘘になる。
//     drawioFrame.ts の dead value 潰しが 1 つ抜けただけでここが赤になる（実測で
//     DRAW_MATH_URL の取りこぼしを捕まえた）。
//  2. 圧縮された `<diagram>`（deflate+base64）も開ける。
//  3. 複数ページを数えて親へ返す（ヘッダの「n / m」表示の材料）。
//  4. ライト/ダークの双方で描ける。
//  5. **要求した暗色描画に本当になっているか。** `"dark-mode"` は真偽値では効かず
//     （isDarkMode() は "dark" / "auto" という文字列と比較する）、true を渡すと黙って
//     ライト描画のまま暗い背景に載り、既定色のラベルが**黒地に黒**になる。判定は
//     ビューアの isDarkMode() を返させて突き合わせる —— **画素を数える判定は当てに
//     ならない**（暗色にならない絵の方が図形の明るい塗りで「明るい画素」が多くなる。
//     実測 40778 対 2387 で、素朴なしきい値は真逆の答えを出した）。
//  6. **ジェスチャが効く。** Ctrl＋ホイール / 2 本指ピンチ / ダブルタップ。GraphViewer は
//     これらを一切配線していない（init は pinchEnabled=false・setPanning(false)）ので、
//     こちらの実装が生きているかは実ブラウザで押してみるしかない。
//  7. **テーマを切り替えても図が壊れない。** drawio は 1 つの文書内でのテーマ往復を
//     想定しておらず、同じフレームに描き直しを頼むと**コンテナ見出しが消え、エッジの
//     ラベルはライト時の白いピル＋黒文字のまま残る**（実測）。DrawioView はフレームごと
//     作り直し、見ていた場所（ページ・倍率・位置）を渡して復元する。ここではその
//     手順どおりに動かし、暗色になっていることと倍率が保たれることを見る。
//  8. **アセットに認証ゲートがあっても描ける。** サンドボックス iframe はオリジンを
//     持たないため、そこからの要求は cross-site 扱いで SameSite=Lax のセッション
//     cookie が付かない。フレームが自分で `<script src>` を取りに行く設計だと CP の
//     authGate に 401 で弾かれ、実機だけが壊れる（2026-08-16 の不具合）。ここでは
//     **cookie 無しの要求を 401 で返す配信**を用意し、それでも描けることを判定する
//     —— ローカルの素の静的配信では絶対に出ない種類の欠陥なので、これが無いと
//     同じ穴に何度でも落ちる。
//
// バンドルも CP も Agent も要らない: フレームは drawioFrame.ts が組み立てる HTML そのもので、
// 素の CDP（Node の global WebSocket）で headless Chromium に読ませる。
// ../pane-heads/check.mjs と同じ流儀。
//
//   npm --prefix console run drawio:check
//   node console/scripts/drawio/check.mjs --screenshot /tmp/drawio.png
//
// **実時間で回す**こと。`--virtual-time-budget` はサンドボックス iframe（別プロセス）を
// 動かさず、ready の後で時間が止まって「描けない」ように見える —— これはハーネスの罠で
// あって製品の不具合ではない。ここで丸一日溶かした。
//
// 終了ステータスが判定: 0 = 全ケース描画・外部通信 0 件。
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.join(HERE, "../../..");
const VIEWER = path.join(REPO, "console/vendor/drawio/viewer-static.min.js");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const CHROME = arg("chrome", process.env.CHROMIUM || "/usr/bin/chromium");
const SHOT = arg("screenshot", "");
const PORT = Number(arg("port", 0)) || 8000 + (process.pid % 1000);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- fixtures
const PLAIN = `<mxfile host="app.diagrams.net">
  <diagram name="ページ1" id="p1"><mxGraphModel dx="800" dy="600" page="1"><root>
    <mxCell id="0"/><mxCell id="1" parent="0"/>
    <mxCell id="n1" value="開始" style="ellipse;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;" vertex="1" parent="1"><mxGeometry x="80" y="40" width="120" height="60" as="geometry"/></mxCell>
    <mxCell id="n2" value="処理する" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="1"><mxGeometry x="80" y="160" width="120" height="60" as="geometry"/></mxCell>
    <mxCell id="n3" value="判定?" style="rhombus;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;" vertex="1" parent="1"><mxGeometry x="60" y="280" width="160" height="80" as="geometry"/></mxCell>
    <mxCell id="e1" style="edgeStyle=orthogonalEdgeStyle;html=1;" edge="1" parent="1" source="n1" target="n2"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="e2" value="はい" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;" edge="1" parent="1" source="n2" target="n3"><mxGeometry relative="1" as="geometry"/></mxCell>
  </root></mxGraphModel></diagram>
  <diagram name="ページ2" id="p2"><mxGraphModel page="1"><root>
    <mxCell id="0"/><mxCell id="1" parent="0"/>
    <mxCell id="m1" value="2枚目" style="whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="160" height="60" as="geometry"/></mxCell>
  </root></mxGraphModel></diagram>
</mxfile>`;

// drawio の圧縮形式: encodeURIComponent → raw deflate → base64。
function compressed(source) {
  const inner = source.match(/<mxGraphModel[\s\S]*?<\/mxGraphModel>/)[0];
  const raw = zlib.deflateRawSync(Buffer.from(encodeURIComponent(inner), "utf8"), { level: 9 });
  return `<mxfile host="app.diagrams.net"><diagram name="圧縮" id="c1">${raw.toString("base64")}</diagram></mxfile>`;
}

// ペインより明らかに大きい図。fit が効いていれば scale < 1 になる。
const BIG = `<mxfile><diagram name="大" id="b1"><mxGraphModel page="1"><root>
  <mxCell id="0"/><mxCell id="1" parent="0"/>
  ${Array.from({ length: 24 }, (_, i) => `<mxCell id="b${i}" value="ノード${i}" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="${(i % 6) * 420}" y="${Math.floor(i / 6) * 320}" width="360" height="220" as="geometry"/></mxCell>`).join("")}
</root></mxGraphModel></diagram></mxfile>`;

const CASES = [
  { name: "plain-light", xml: PLAIN, dark: false, pages: 2, scale: 1, gestures: true },
  // 暗いテーマは「既定色の文字が読めるか」まで見る（真偽値を渡すと黒地に黒になる）。
  { name: "plain-dark", xml: PLAIN, dark: true, pages: 2, scale: 1 },
  { name: "compressed", xml: compressed(PLAIN), dark: false, pages: 1, scale: 1 },
  { name: "big", xml: BIG, dark: false, pages: 1, maxScale: 0.5 },
  // 図として読めない XML。ビューアの英語メッセージを黙って出すのではなく、
  // 親が扱えるイベントとして返ってこなければならない。
  { name: "not-a-diagram", xml: "<foo><bar/></foo>", dark: false, expectError: true },
  // ライトで開き、ズームしてから、テーマ切り替え（＝フレーム作り直し）を通す。
  { name: "theme-switch", xml: PLAIN, dark: false, pages: 2, themeSwitch: true },
];

// ---------------------------------------------------------------- frame html
// drawioFrame.ts をそのまま使う（ハーネスが自前で HTML を書くと、守っている対象が
// 製品コードでなくなる）。esbuild は vite の依存として node_modules にある。
async function loadFrameBuilder() {
  const out = path.join(os.tmpdir(), `af-drawio-frame-${process.pid}.cjs`);
  const esbuild = path.join(REPO, "console/node_modules/.bin/esbuild");
  await new Promise((res, rej) => {
    const p = spawn(esbuild, [path.join(REPO, "console/src/features/viewer/drawioFrame.ts"), "--format=cjs", `--outfile=${out}`], { stdio: "inherit" });
    p.on("exit", (c) => (c === 0 ? res() : rej(new Error("esbuild failed"))));
  });
  const mod = await import(`file://${out}`);
  return mod.default ?? mod;
}

// ---------------------------------------------------------------- static server
// CP を模した配信。**`/assets/*` はセッション cookie が無ければ 401**（authGate と
// 同じ形）。ページ側は Lax cookie を配るので、親の fetch には付き、オリジンを持たない
// フレームからの要求には付かない —— 実機で起きたことがそのまま再現する。
function serve(dir, port) {
  return new Promise((resolve) => {
    import("node:http").then(({ createServer }) => {
      const srv = createServer((req, res) => {
        const url = req.url.split("?")[0];
        const gated = url.startsWith("/assets/");
        if (gated && !(req.headers.cookie || "").includes("af_session=")) {
          unauthorized.push(url);
          res.writeHead(401, { "Content-Type": "application/json" }).end('{"error":"unauthenticated"}');
          return;
        }
        const file = path.join(dir, decodeURIComponent(gated ? url.slice("/assets/".length) : url));
        fs.readFile(file, (err, buf) => {
          if (err) {
            res.writeHead(404).end("no");
            return;
          }
          res.writeHead(200, {
            "Content-Type": (file.endsWith(".js") ? "text/javascript" : "text/html") + "; charset=utf-8",
            // CP と同じ SameSite=Lax。これが無いと欠陥を再現できない。
            "Set-Cookie": "af_session=harness; Path=/; HttpOnly; SameSite=Lax",
          }).end(buf);
        });
      });
      srv.listen(port, "127.0.0.1", () => resolve(srv));
    });
  });
}

/** cookie 無しで拒否した要求（＝フレームが自分で取りに行った証拠）。 */
const unauthorized = [];

// ---------------------------------------------------------------- CDP
async function browser() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-drawio-"));
  const proc = spawn(
    CHROME,
    [
      "--headless",
      "--disable-gpu",
      "--no-sandbox",
      "--hide-scrollbars",
      "--remote-debugging-address=127.0.0.1",
      // 固定ポートは同じコンテナの別セッションの Chromium を掴む事故になる。0 を取り、
      // 実際に取れたポートは DevToolsActivePort から読む。
      "--remote-debugging-port=0",
      `--user-data-dir=${dir}`,
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  let port = "";
  for (let i = 0; i < 100 && !port; i++) {
    await sleep(100);
    try {
      port = fs.readFileSync(path.join(dir, "DevToolsActivePort"), "utf8").split("\n")[0];
    } catch {}
  }
  if (!port) throw new Error("chromium did not report a debugging port");
  const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })).json();
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  const events = [];
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m.result);
      pending.delete(m.id);
    } else if (m.method) events.push(m);
  };
  const call = (method, params = {}) =>
    new Promise((res) => {
      const i = ++id;
      pending.set(i, res);
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  return { call, events, close: () => (ws.close(), proc.kill(), fs.rmSync(dir, { recursive: true, force: true })) };
}

// ---------------------------------------------------------------- run
const fail = [];
const { drawioFrameSrcdoc } = await loadFrameBuilder();
const work = fs.mkdtempSync(path.join(os.tmpdir(), "af-drawio-www-"));
fs.copyFileSync(VIEWER, path.join(work, "viewer.js"));
const srv = await serve(work, PORT);
const base = `http://127.0.0.1:${PORT}`;
const b = await browser();
await b.call("Page.enable");
// スクリーンショットは既定ウィンドウ（780px 幅）で切られる。フレームより広く取る。
await b.call("Emulation.setDeviceMetricsOverride", { width: 900, height: 600, deviceScaleFactor: 1, mobile: false });
await b.call("Network.enable");

for (const c of CASES) {
  const srcdoc = drawioFrameSrcdoc({ dark: c.dark });
  // 親の手順は DrawioView.tsx と同じにする（ここが実機とずれると、守っている対象が
  // 製品コードでなくなる）: ready を待つ → 自分が取ったビューア本文を boot で渡す →
  // booted を待って render。**フレームは 1 本も要求を出さない。**
  const page = `<!doctype html><html><head><meta charset="utf-8"></head><body style="margin:0;background:${c.dark ? "#1e1e1e" : "#fff"}">
<iframe id="f" sandbox="allow-scripts" width="860" height="520" style="border:0;display:block" srcdoc="${srcdoc.replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"></iframe>
<script>
window.__ev=[];
function post(m){ m.af="af-drawio"; document.getElementById("f").contentWindow.postMessage(m,"*"); }
window.__dark=${c.dark}; window.__restore=null;
window.addEventListener("message",function(e){ var d=e.data; if(!d||d.af!=="af-drawio")return; window.__ev.push(d);
  if(d.t==="ready") fetch("/assets/viewer.js",{credentials:"same-origin"}).then(function(r){return r.text()}).then(function(src){ post({t:"boot",src:src}); });
  if(d.t==="booted") post({t:"render",xml:${JSON.stringify(c.xml)},dark:window.__dark,restore:window.__restore?JSON.parse(window.__restore):null});
});
</script></body></html>`;
  fs.writeFileSync(path.join(work, `${c.name}.html`), page);

  b.events.length = 0;
  unauthorized.length = 0;
  await b.call("Page.navigate", { url: `${base}/${c.name}.html` });
  // 実時間で待つ（先頭のコメント参照）。4MB のビューアの読み込み + 描画で十分な余裕。
  await sleep(3500);

  const got = await b.call("Runtime.evaluate", { expression: "JSON.stringify(window.__ev)", returnByValue: true });
  const evs = JSON.parse(got.result?.value || "[]");
  const rendered = evs.filter((e) => e.t === "rendered").pop();
  const errors = evs.filter((e) => e.t === "error");

  if (c.expectError) {
    if (!errors.length) fail.push(`${c.name}: エラーとして返ってこなかった (events=${JSON.stringify(evs)})`);
  } else {
    if (!rendered) fail.push(`${c.name}: 描画されなかった (events=${JSON.stringify(evs)})`);
    else if (rendered.pages !== c.pages) fail.push(`${c.name}: ページ数 ${rendered.pages} (期待 ${c.pages})`);
    else if (c.scale && rendered.scale !== c.scale) fail.push(`${c.name}: 倍率 ${rendered.scale} (期待 ${c.scale})`);
    else if (c.maxScale && !(rendered.scale < c.maxScale)) fail.push(`${c.name}: 縮小されていない (scale=${rendered.scale})`);
    if (errors.length) fail.push(`${c.name}: エラー ${JSON.stringify(errors)}`);
  }

  // 外部通信の検査。自分の静的サーバ以外への要求は 1 本も出てはならない。
  const external = b.events
    .filter((e) => e.method === "Network.requestWillBeSent")
    .map((e) => e.params.request.url)
    .filter((u) => !u.startsWith(base) && !u.startsWith("data:") && !u.startsWith("about:") && !u.startsWith("blob:"));
  if (external.length) fail.push(`${c.name}: 外部への要求 ${JSON.stringify([...new Set(external)])}`);
  // cookie の付かない要求 ＝ オリジンを持たないフレームが自分で取りに行った要求。
  if (unauthorized.length) {
    fail.push(`${c.name}: フレームが資格情報無しで取りに行った ${JSON.stringify([...new Set(unauthorized)])}`);
  }

  // ── テーマ切り替え（docs/65 §65.11-12）──────────────────────────────────
  // DrawioView と同じ手順: 拡大しておく → フレームを作り直す（新しい srcdoc）→
  // ready から boot し直し → 直前の現在地を restore で渡す。
  if (c.themeSwitch && rendered) {
    // まず利用者の操作を模して拡大する（引き継ぎの対象を作る）。
    await b.call("Input.dispatchMouseEvent", { type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240, modifiers: 2 });
    await sleep(400);
    const before = await b.call("Runtime.evaluate", {
      expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop())",
      returnByValue: true,
    });
    const kept = JSON.parse(before.result?.value || "null");

    const darkDoc = drawioFrameSrcdoc({ dark: true }).replace(/&/g, "&amp;").replace(/"/g, "&quot;");
    await b.call("Runtime.evaluate", {
      expression: `(() => {
        const old = document.getElementById("f");
        const next = document.createElement("iframe");
        next.id = "f"; next.width = "860"; next.height = "520";
        next.style.cssText = "border:0;display:block";
        next.setAttribute("sandbox", "allow-scripts");
        window.__dark = true;
        window.__restore = ${JSON.stringify(JSON.stringify(kept))};
        next.srcdoc = ${JSON.stringify(darkDoc.replace(/&amp;/g, "&").replace(/&quot;/g, '"'))};
        old.replaceWith(next);
        window.__ev.length = 0;
        return true;
      })()`,
      returnByValue: true,
    });
    await sleep(4000);

    const after = await b.call("Runtime.evaluate", {
      expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop())",
      returnByValue: true,
    });
    const now = JSON.parse(after.result?.value || "null");
    if (!now) fail.push(`${c.name}: 作り直したフレームが描画しなかった`);
    else {
      if (!now.darkMode) fail.push(`${c.name}: 作り直し後に暗色になっていない`);
      if (Math.abs(now.scale - kept.scale) > 0.01) {
        fail.push(`${c.name}: 倍率が引き継がれていない (${kept.scale} → ${now.scale})`);
      }
      if (now.pageId !== kept.pageId) fail.push(`${c.name}: ページが引き継がれていない (${kept.pageId} → ${now.pageId})`);
      console.log(`   theme-switch: scale ${kept.scale} → ${now.scale} / page ${now.pageId} / darkMode=${now.darkMode}`);
    }
  }

  // ── ジェスチャ（docs/65 §65.12）────────────────────────────────────────
  // フレームは倍率が変わるたびに rendered を返すので、押した結果は親から観測できる。
  if (c.gestures && rendered) {
    const scaleNow = async () => {
      const r = await b.call("Runtime.evaluate", {
        expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop()||null)",
        returnByValue: true,
      });
      return JSON.parse(r.result?.value || "null")?.scale ?? null;
    };
    const base0 = await scaleNow();

    // Ctrl＋ホイール（modifiers: 2 = Ctrl）。トラックパッドのピンチも同じ経路。
    await b.call("Input.dispatchMouseEvent", {
      type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240, modifiers: 2,
    });
    await sleep(400);
    const afterWheel = await scaleNow();
    if (!(afterWheel > base0)) fail.push(`${c.name}: Ctrl+ホイールで拡大しない (${base0} → ${afterWheel})`);

    // 素のホイールは倍率を変えない（パンのみ）。
    await b.call("Input.dispatchMouseEvent", { type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240 });
    await sleep(300);
    const afterPan = await scaleNow();
    if (afterPan !== afterWheel) fail.push(`${c.name}: 素のホイールで倍率が動いた (${afterWheel} → ${afterPan})`);

    // 2 本指ピンチ（広げる = 拡大）。タッチを有効にしてから送る。
    await b.call("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
    await b.call("Input.dispatchTouchEvent", {
      type: "touchStart", touchPoints: [ { x: 380, y: 260, id: 1 }, { x: 480, y: 260, id: 2 } ],
    });
    for (const spread of [40, 80, 120]) {
      await b.call("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [ { x: 430 - spread, y: 260, id: 1 }, { x: 430 + spread, y: 260, id: 2 } ],
      });
      await sleep(60);
    }
    await b.call("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(400);
    const afterPinch = await scaleNow();
    if (!(afterPinch > afterPan)) fail.push(`${c.name}: ピンチで拡大しない (${afterPan} → ${afterPinch})`);

    // ダブルタップ = 収まりへ戻る（拡大している状態なので fit に落ちる）。
    for (let i = 0; i < 2; i++) {
      await b.call("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 430, y: 260, id: 3 }] });
      await b.call("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
      await sleep(60);
    }
    await sleep(500);
    const afterTap = await scaleNow();
    if (!(afterTap < afterPinch)) fail.push(`${c.name}: ダブルタップで収め直さない (${afterPinch} → ${afterTap})`);
    await b.call("Emulation.setTouchEmulationEnabled", { enabled: false });
    console.log(`   gestures: fit=${base0} → ctrl+wheel=${afterWheel} → pinch=${afterPinch} → dbltap=${afterTap}`);
  }

  // ── コントラスト（docs/65 §65.12）────────────────────────────────────
  // 暗いテーマで「既定色の文字」が背景に溶けていないか。画素を数えて判定する。
  if (rendered && rendered.darkMode !== c.dark) {
    fail.push(
      `${c.name}: 要求 dark=${c.dark} に対しビューアは darkMode=${rendered.darkMode}` +
        `（"dark-mode" に真偽値を渡すと黙ってライト描画になる）`,
    );
  }

  if (SHOT) {
    const shot = await b.call("Page.captureScreenshot", {});
    const file = SHOT.replace(/\.png$/, "") + `-${c.name}.png`;
    fs.writeFileSync(file, Buffer.from(shot.data, "base64"));
    console.log(`  screenshot: ${file}`);
  }
  console.log(`${fail.length ? "…" : "ok"} ${c.name}: ${JSON.stringify(rendered || null)}`);
}

b.close();
srv.close();
fs.rmSync(work, { recursive: true, force: true });

if (fail.length) {
  console.error("\nFAIL:\n - " + fail.join("\n - "));
  process.exit(1);
}
console.log("\nall good: 同梱ビューアだけで描画・外部通信 0 件");
