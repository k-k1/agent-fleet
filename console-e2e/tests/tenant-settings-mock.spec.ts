// テナント設定モーダル（docs/61 の面の置き場を管理モーダルから移したもの）を、実ビルド
// （console/dist）を headless で動かして固定する。CP も Docker も要らない mock 系
// （plan-comments-mock.spec.ts と同じ骨格）。
//
// 守りたいのは「画面の出し分けをサーバの権限より緩めない」こと。テナント管理者
// （super_admin: false）で:
//   - アカウントメニューからテナント設定へ入れる
//   - サインイン方式は編集できるが「承認して有効化」は出ない（承認は決定 30 でデプロイ
//     管理者だけ。実体は CP の setStatus が見ており、ここは案内）
//   - ログイン規則は読み取り専用（PUT が withSuperAdmin 固定・決定 19）
// jsdom 側（console/src/features/settings/TenantDialog.dom.test.tsx）は同じ条件を
// コンポーネント単体で見るが、アカウントメニューからの導線はバンドルを動かさないと
// 確かめられない。
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect } from "@playwright/test";

const root = path.resolve(__dirname, "../..");
const dist = path.join(root, "console", "dist");
let server: http.Server;
let origin = "";

const TENANT = {
  slug: "acme",
  name: "Acme",
  users: 3,
  running: 0,
  allowed_providers: "entra",
  auto_join_domains: "@sales.acme.co.jp",
  allowed_domains: "",
};
const IDP = {
  id: "idp1",
  name: "entra",
  issuer: "https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/v2.0",
  client_id: "cid",
  trust: "issuer",
  allowed_domains: "@acme.co.jp",
  status: "pending",
  has_secret: true,
  usable: false,
};

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
  if (!server) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

test("テナント管理者: サインイン方式は編集できるが承認は出ず、ログイン規則は読み取り専用", async ({
  page,
}) => {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "alice", email: "alice@acme.co.jp" } });
    // 一覧の role がアカウントメニューの「テナント設定」の出し分けを決める。
    if (p === "/api/tenants")
      return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: false } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    // ★ isSuper の出どころ。テナント管理者だけの人には super_admin: false が返る。
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: false } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP] } });
    // モックしていない API は握り潰さず落とす（path のタイポ／API 変更の検知）。
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  await expect(modal).toBeVisible();
  // サインイン方式（既定セクション）。行は出る＝テナント管理者も自分の IdP を扱える。
  await expect(modal.locator(".adm-mcp-row .as-name")).toHaveText("entra");
  await expect(modal.locator(".idp-state")).toHaveText("承認待ち");
  const acts = modal.locator(".allow-acts button");
  await expect(acts.filter({ hasText: "編集" })).toHaveCount(1);
  await expect(acts.filter({ hasText: "承認して有効化" })).toHaveCount(0);

  // ログイン規則: 値は読めるが、入力欄も保存ボタンも無い。
  await modal.locator(".settings-rail-item", { hasText: "ログイン規則" }).click();
  await expect(modal.locator(".af-val")).toHaveCount(3);
  await expect(modal.locator(".af-val").nth(0)).toHaveText("entra");
  await expect(modal.locator(".af-val").nth(2)).toContainText("未設定");
  await expect(modal.locator(".settings-content input")).toHaveCount(0);
  await expect(modal.locator(".settings-content .admin-actions")).toHaveCount(0);
});
