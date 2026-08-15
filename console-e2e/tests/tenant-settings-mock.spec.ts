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
  tenant_slug: "acme",
};
// テナント定義の GitHub 行（docs/61 §61.15）。issuer はサーバが入れた定数で、
// 行の身元を分けているのは org（＋ドメイン台帳）。
const GITHUB_IDP = {
  id: "idp2",
  name: "github",
  kind: "github",
  issuer: "https://github.com",
  client_id: "gh-cid",
  trust: "api",
  allowed_orgs: "acme-sub",
  allowed_domains: "@sub.acme.co.jp",
  status: "active",
  usable: true,
  has_secret: true,
  tenant_slug: "acme",
};
const MEMBERS = [
  { user_key: "tanaka", email: "tanaka@acme.co.jp", role: "tenant_admin", state: "running", max_sessions: 5 },
  { user_key: "suzuki", email: "suzuki@acme.co.jp", role: "member", state: "stopped" },
];
const SESSIONS = [
  { name: "wip-a", label: "[AF] 認証の修正", kind: "claude", repo: "acme/web", tenant: "acme", user_key: "tanaka", alive: true, state: "running", started: "10:12" },
];

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
  // 4 行目は「ボタンを出さない方式」（docs/61 §61.15.9）— 受け入れる方式とは別の欄。
  await expect(modal.locator(".af-val")).toHaveCount(4);
  await expect(modal.locator(".af-val").nth(0)).toHaveText("entra");
  await expect(modal.locator(".af-val").nth(2)).toContainText("未設定");
  await expect(modal.locator(".af-val").nth(3)).toContainText("未設定");
  await expect(modal.locator(".settings-content input")).toHaveCount(0);
  await expect(modal.locator(".settings-content .admin-actions")).toHaveCount(0);
});

// 管理モーダルを super_admin 専用に閉じた分、tenant_admin が失う面がないことを、
// バンドルを動かして確かめる。jsdom 側はコンポーネント単体なので、アカウントメニューの
// 出し分けと「移した先で本当に描けるか」はここでしか見られない。
test("テナント管理者: 「管理」は消え、メンバーと運用はテナント設定から届く", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "alice", email: "alice@acme.co.jp" } });
    if (p === "/api/tenants")
      return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: false } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: false } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP] } });
    if (p === "/api/admin/tenants/acme/members") return route.fulfill({ json: { members: MEMBERS } });
    if (p.endsWith("/members/tanaka/stats"))
      return route.fulfill({ json: { running: true, mem_used: 3435973836, mem_max: 8589934592, cpu_pct: 12 } });
    if (p.endsWith("/members/tanaka/sessions")) return route.fulfill({ json: { sessions: SESSIONS } });
    if (p === "/api/admin/sessions") return route.fulfill({ json: { sessions: SESSIONS } });
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  const menu = page.locator(".acct-menu");
  // ★ 管理はデプロイ全体の面（CP は withSuperAdmin 固定）。押した先が 403 になる
  // 入口を出さない。
  await expect(menu.locator(".acct-item", { hasText: "管理" })).toHaveCount(0);
  await menu.locator(".acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  // メンバー: 一覧 → 詳細 → 戻る。段は本文の中で積む（レールは「メンバー」のまま）。
  await modal.locator(".settings-rail-item", { hasText: "メンバー" }).first().click();
  await expect(modal.locator(".member-row")).toHaveCount(2);
  await modal.locator(".member-row").first().click();
  await expect(modal.locator(".member-detail")).toBeVisible();
  // offboarding 一式は tenant_admin のもの（docs/61 §61.10.6 / 決定 26）。
  await expect(modal.locator(".member-detail button", { hasText: "メンバーを外す" })).toHaveCount(1);
  await expect(modal.locator(".member-detail button", { hasText: "home を掃除" })).toHaveCount(1);
  // 権限（tenant_admin の付与）はデプロイ管理者のものなので出ない。
  await expect(modal.locator(".member-detail button", { hasText: "テナント管理者にする" })).toHaveCount(0);
  await modal.locator(".tenant-drill .admin-back").click();
  await expect(modal.locator(".member-detail")).toHaveCount(0);
  await expect(modal.locator(".member-row")).toHaveCount(2);

  // 運用: この画面はテナントを跨がないので、テナントを選ぶ欄は出さない。
  await modal.locator(".settings-rail-item", { hasText: "セッション" }).click();
  await expect(modal.locator(".adm-session")).toHaveCount(1);
  await expect(modal.locator(".usage-toolbar select")).toHaveCount(0);
});

// 承認は台帳の行から打てる（それまでは件数だけ出して、承認はテナント詳細まで
// 降りる必要があった）。宛先はその行の tenant_slug で組む。
test("デプロイ管理者: 登録簿の行から承認して有効化できる", async ({ page }) => {
  let approved: { path: string; body: unknown } | null = null;
  await page.route("**/api/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    if (req.method() === "POST" && p === "/api/admin/tenants/acme/idp/idp1/status") {
      approved = { path: p, body: req.postDataJSON() };
      return route.fulfill({ json: {} });
    }
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "root", email: "root@example.com" } });
    if (p === "/api/tenants") return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: true } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: true } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP] } });
    if (p === "/api/admin/idp")
      return route.fulfill({ json: { providers: [approved ? { ...IDP, status: "active", usable: true } : IDP] } });
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "管理" }).click();

  const register = page.locator(".admin-panel", { hasText: "テナント定義のサインイン方法" });
  await expect(register).toBeVisible();
  await register.locator("button", { hasText: "承認して有効化" }).click();

  expect(approved).toEqual({ path: "/api/admin/tenants/acme/idp/idp1/status", body: { status: "active" } });
  // 押したあとは読み直す — 承認済みの行は「停止する」に変わる（台帳は空にならない）。
  await expect(register.locator("button", { hasText: "停止する" })).toHaveCount(1);
  await expect(register.locator("button", { hasText: "承認して有効化" })).toHaveCount(0);
});

// ★ kind で「何を訊くか」が変わる（docs/61 §61.15）。GitHub 行に issuer / tid /
// 信頼方法を出すと、埋めようのない欄を見せて保存時 400 になり、逆に組織を出さないと
// 必須欄が無い。一覧の「身元の出どころ」も、github.com は全テナント同じで何も区別
// できないので org を出す。そして種類を戻したときに issuer を持ち越さないこと —
// 持ち越すと「issuer が https://github.com の OIDC 行」という、保存はできるのに
// 動かない行が作れてしまう。
test("テナント管理者: GitHub 行は組織を訊き、issuer / tid / 信頼方法を出さない", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "alice", email: "alice@acme.co.jp" } });
    if (p === "/api/tenants")
      return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: false } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: false } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP, GITHUB_IDP] } });
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  const rows = modal.locator(".adm-mcp-row");
  await expect(rows).toHaveCount(2);
  // 身元の出どころ: OIDC は issuer、GitHub は組織。
  await expect(rows.nth(0).locator(".as-repo")).toHaveText(IDP.issuer);
  await expect(rows.nth(1).locator(".as-name")).toHaveText("github");
  await expect(rows.nth(1).locator(".as-repo")).toHaveText("GitHub: acme-sub");

  // github 行を編集すると、欄が入れ替わる。
  await rows.nth(1).locator("button", { hasText: "編集" }).click();
  const form = modal.locator(".adm-mcp-form");
  const labels = form.locator(".ssm-fld > label");
  await expect(labels.filter({ hasText: "許可する GitHub 組織" })).toHaveCount(1);
  await expect(form.locator(".ssm-fld input").nth(1)).toHaveValue("acme-sub");
  await expect(labels.filter({ hasText: "issuer" })).toHaveCount(0);
  await expect(labels.filter({ hasText: "email の信頼方法" })).toHaveCount(0);
  await expect(labels.filter({ hasText: "許可する Entra テナント" })).toHaveCount(0);
  // ドメインは GitHub でも必須のまま（1 ドメイン 1 テナントの台帳・§61.15.3）。
  await expect(labels.filter({ hasText: "受け入れるメールドメイン" })).toHaveCount(1);

  // 種類を自社 IdP に戻すと issuer 欄が現れ、github.com は持ち越されない。
  await form.locator("select").first().selectOption("oidc");
  await expect(labels.filter({ hasText: "許可する GitHub 組織" })).toHaveCount(0);
  const issuer = form.locator(".ssm-fld", { hasText: "issuer（発行者 URL）" }).locator("input");
  await expect(issuer).toHaveValue("");
});

// 「使えるサインイン方法」は自由入力で、何が書けるかは env にしか無かった（打ち間違えると
// CP が 400 unknown_provider で弾くだけ）。GET /api/admin/providers を欄のすぐ下に出す。
// バンドルを動かして見るのは、置き場（規則のパネルの中）と、tenant_admin の面に漏れて
// いないことが、コンポーネント単体では確かめられないため。
test("デプロイ管理者: ログイン規則の欄に、書ける provider id が並ぶ", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "root", email: "root@example.com" } });
    if (p === "/api/tenants") return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: true } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: true } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP] } });
    if (p === "/api/admin/tenants/acme/members") return route.fulfill({ json: { members: MEMBERS } });
    if (p === "/api/admin/idp") return route.fulfill({ json: { providers: [IDP] } });
    // 秘密は載らない（CP 側 login_provider_api.go が id・表示名・issuer だけを返す）。
    if (p === "/api/admin/providers")
      return route.fulfill({
        json: {
          providers: [
            { id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google", issuer: "https://accounts.google.com" },
            { id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft", issuer: IDP.issuer },
          ],
        },
      });
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "管理" }).click();
  await page.locator(".tc-name", { hasText: "Acme" }).first().click();

  // 答えは欄と同じパネルの中にある（別の面に置くと、弾かれた人が辿り着けない）。
  const rules = page.locator(".admin-panel", { hasText: "ログイン規則" });
  const known = rules.locator(".idp-known");
  await expect(known.locator(".adm-mcp-row")).toHaveCount(2);
  await expect(known.locator(".adm-mcp-row").nth(1).locator(".as-name")).toHaveText("Microsoft でサインイン");
  await expect(known.locator(".adm-mcp-row").nth(1).locator("code")).toHaveText("entra");
  await expect(known.locator(".adm-mcp-row").nth(1).locator(".as-repo")).toHaveText(IDP.issuer);
});
