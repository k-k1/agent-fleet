// ファイルセクションの再帰検索（home スコープ）を、**実 console ビルド + 実ブラウザ**に
// モックバックエンドで当てる。editor-mock.spec.ts と同じ「dist を静的配信 + /api/** を
// route で差し替え」方式なので **docker もイメージも要らない** — console.spec.ts（実 CP・
// 実コンテナ）が回せない手元でも、CI の image ジョブより先にここで落ちる。
//
// なぜ要るか: この経路は一度「クエリを打った瞬間に Console 全体が白紙化する」壊れ方を
// している（ProjectFiles の sticky lineage 用 layout effect が searchMode 突入時に
// 毎レンダー再実行 → 無ガードの setSticky が毎回新オブジェクトを積む → React
// "maximum update depth"(#185) で root ごと unmount）。**症状は「行が出ない」ではなく
// 「アプリが消える」**で、console.spec.ts は `.fsrow` が見つからないとしか言えず、
// 失敗スクショも真っ黒なので原因が読めなかった。ここでは pageerror と #root の中身を
// 直接見張り、白紙化を白紙化として報告する。
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect } from "@playwright/test";

const root = path.resolve(__dirname, "../..");
const dist = path.join(root, "console", "dist");
let server: http.Server;
let origin = "";

// home 直下に置かれた実ファイル相当（repos の外＝home スコープでしか出ない）。
const MARKER = "ui-search-mock.txt";

test.beforeAll(async () => {
  server = http.createServer((req, res) => {
    const pathname = new URL(req.url || "/", "http://local").pathname;
    const relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
    const file = path.join(dist, relative);
    if (!file.startsWith(dist) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      res.writeHead(404).end();
      return;
    }
    const type = file.endsWith(".html")
      ? "text/html"
      : file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : "application/octet-stream";
    res.writeHead(200, { "content-type": type });
    fs.createReadStream(file).pipe(res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});

test.afterAll(async () => {
  // beforeAll が listen 前に失敗した場合 server は未生成（editor-mock と同じガード）。
  if (!server) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

test("ファイル検索: home スコープの再帰検索が行を出し、root を白紙化しない", async ({ page }) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  const searchCalls: string[] = [];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev" } });
    if (p === "/api/tenants") return route.fulfill({ json: { tenants: [] } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    // 実 e2e ワークスペースと同じ「repos は空」状態（クローン無し）。
    if (p === "/api/fs/tree") return route.fulfill({ json: { entries: [] } });
    if (p === "/api/fs/search") {
      searchCalls.push(url.search);
      const q = url.searchParams.get("q") || "";
      // 実バックエンド（rg）と同じ意味論: home スコープ = path 空、部分一致。
      const hit = url.searchParams.get("path") === "" && q !== "" && MARKER.includes(q);
      return route.fulfill({ json: { results: hit ? [MARKER] : [], truncated: false } });
    }
    // 明示的にモックした path 以外は握り潰さず abort（editor-mock と同じ理由: モック側/
    // アプリ側の path タイポや API 変更を、空 200 で緑にせず検知する）。左ペインの
    // シェルは未知 API が全部 abort でも描画される（実測）。
    return route.abort();
  });

  await page.goto(origin);

  const files = page.locator(".ui-section", { has: page.locator(".ui-section-title", { hasText: /ファイル|Files/ }) });
  await expect(files).toHaveCount(1);
  const toggle = files.locator(".ui-section-toggle");
  if ((await toggle.getAttribute("aria-expanded")) !== "true") await toggle.click();

  // スコープを home へ（既定は repos）。位置指定 nth() ではなく aria-label 由来の
  // アクセシブル名で引く — ボタンが増減しても意味で当たるように。
  const homeScope = files.getByRole("button", { name: /home から検索|Search from home/ });
  await homeScope.click();
  await expect(homeScope).toHaveAttribute("aria-pressed", "true");

  await files.locator(".proj-filter input").fill(MARKER);

  // ヒット行が出る（= 検索が実際に投げられ、フラット結果が描画される）。
  await expect(files.locator(`.fsrow[data-path="${MARKER}"]`)).toBeVisible({ timeout: 15_000 });
  expect(searchCalls.some((s) => s.includes("path=&"))).toBeTruthy();

  // 白紙化の直接ガード。上の行アサートだけだと、無限ループで root が unmount した
  // 場合も「要素が無い」で落ちるだけで、原因が「アプリが消えた」ことだと分からない。
  expect(pageErrors).toEqual([]);
  expect((await page.locator("#root").innerHTML()).length).toBeGreaterThan(0);

  // 検索を消したら通常ツリー表示へ戻れる（searchMode の出入りが両方向で安定なこと。
  // 入る側だけ直して出る側で同じループを踏む、を防ぐ）。
  await files.locator(".proj-filter input").fill("");
  await expect(files.locator(".proj-filter input")).toHaveValue("");
  expect(pageErrors).toEqual([]);
  expect((await page.locator("#root").innerHTML()).length).toBeGreaterThan(0);
});
