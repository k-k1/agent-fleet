// テナント設定モーダル（docs/log/61 の面の置き場を管理モーダルから移したもの）を、実ビルド
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
// テナント定義の GitHub 行（docs/log/61 §61.15）。issuer はサーバが入れた定数で、
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
    // 一覧はテナント管理者にも開いている（§61.17.9 ①）。ただし issuer は super_admin
    // にしか返らないので、ここでも載せない。
    if (p === "/api/admin/providers")
      return route.fulfill({ json: { providers: [{ id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google" }] } });
    // モックしていない API は握り潰さず落とす（path のタイポ／API 変更の検知）。
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  await expect(modal).toBeVisible();
  // サインイン方式（既定セクション）。デプロイ共通の方式と自前の行が 1 本のリストに
  // 並ぶ（docs/log/61 §61.17.5）。
  const rows = modal.locator(".adm-mcp-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0).locator(".as-name")).toHaveText("Google でサインイン");
  await expect(rows.nth(0).locator("code")).toHaveText("google");
  await expect(rows.nth(0).locator(".idp-state")).toHaveText("デプロイ共通");
  // 自前の行は出る＝テナント管理者も自分の IdP を扱える。
  await expect(rows.nth(1).locator(".as-name")).toHaveText("entra");
  await expect(rows.nth(1).locator(".idp-state")).toHaveText("承認待ち");
  const acts = modal.locator(".allow-acts button");
  await expect(acts.filter({ hasText: "編集" })).toHaveCount(1);
  await expect(acts.filter({ hasText: "承認して有効化" })).toHaveCount(0);
  // ★ 2 トグルは状態として見えるが倒せない（規則の PUT は withSuperAdmin 固定）。
  // 押せないチェックボックスではなく、静的なチップとして出す。
  await expect(modal.locator(".idp-flags .idp-flag")).toHaveCount(2);
  await expect(modal.locator(".idp-flags input")).toHaveCount(0);

  // ログイン規則: 値は読めるが、入力欄も保存ボタンも無い。
  await modal.locator(".settings-rail-item", { hasText: "ログイン規則" }).click();
  // 方式の 2 列は P7-0 でこの欄から出た（§61.17.5）— 残るのはドメインの 2 列だけで、
  // 方式は「サインイン方式」の面を指す 1 行のヒントになる。
  await expect(modal.locator(".af-val")).toHaveCount(2);
  await expect(modal.locator(".af-val").nth(0)).toHaveText("@sales.acme.co.jp");
  await expect(modal.locator(".af-val").nth(1)).toContainText("未設定");
  await expect(modal.locator(".admin-hint", { hasText: "「サインイン方法」の面で行ごとに切り替えます" })).toHaveCount(1);
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
  // offboarding 一式は tenant_admin のもの（docs/log/61 §61.10.6 / 決定 26）。
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
  // 管理モーダルは左レール＋本文（テナント一覧が着地点）。台帳はレールの 1 項目。
  await page.locator(".settings-rail-item", { hasText: "サインイン方法の登録簿" }).click();

  const register = page.locator(".admin-panel", { hasText: "テナント定義のサインイン方法" });
  await expect(register).toBeVisible();
  await register.locator("button", { hasText: "承認して有効化" }).click();

  expect(approved).toEqual({ path: "/api/admin/tenants/acme/idp/idp1/status", body: { status: "active" } });
  // 押したあとは読み直す — 承認済みの行は「停止する」に変わる（台帳は空にならない）。
  await expect(register.locator("button", { hasText: "停止する" })).toHaveCount(1);
  await expect(register.locator("button", { hasText: "承認して有効化" })).toHaveCount(0);
});

// ★ kind で「何を訊くか」が変わる（docs/log/61 §61.15）。GitHub 行に issuer / tid /
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
  // ★ 「同一アカウントの見分け方」も GitHub には無い（docs/log/61 §61.15.11）。GitHub の
  // subject はどの OAuth App でも同じ数値 id なので、2 本目の鍵が要らない。
  await expect(labels.filter({ hasText: "同一アカウントの見分け方" })).toHaveCount(0);
  // ドメインは GitHub でも必須のまま（1 ドメイン 1 テナントの台帳・§61.15.3）。
  await expect(labels.filter({ hasText: "受け入れるメールドメイン" })).toHaveCount(1);

  // 種類を自社 IdP に戻すと issuer 欄が現れ、github.com は持ち越されない。
  await form.locator("select").first().selectOption("oidc");
  await expect(labels.filter({ hasText: "許可する GitHub 組織" })).toHaveCount(0);
  // OIDC 側には出る。★ 自由入力ではなく選択で、選べるのは CP が許した名前だけ
  // （email などを書けると、同じ発行元を共有する方式が既存アカウントに届く）。
  const linkClaim = form.locator(".ssm-fld", { hasText: "同一アカウントの見分け方" }).locator("select");
  await expect(linkClaim).toHaveCount(1);
  await expect(linkClaim.locator("option")).toHaveText(["既定（sub で見分ける）", "oid"]);
  const issuer = form.locator(".ssm-fld", { hasText: "issuer（発行者 URL）" }).locator("input");
  await expect(issuer).toHaveValue("");
});

// 「使えるサインイン方法」はかつて自由入力の CSV で、何が書けるかは env にしか無かった
// （打ち間違えると CP が 400 unknown_provider で弾くだけ）。P7-0 でその欄は消え、
// GET /api/admin/providers が返すデプロイ共通の方式が**行として**並ぶようになった
// （docs/log/61 §61.17.5）。バンドルを動かして見るのは、置き場（サインイン方式の面）と、
// 自前の行と 1 本のリストに混ざる並び順が、コンポーネント単体では確かめられないため。
test("デプロイ管理者: デプロイ共通の方式が、テナントの一覧に id つきで並ぶ", async ({ page }) => {
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
  // テナントを開くとレールごと入れ替わる（着地は「上限」）。方式はログインの節。
  await page.locator(".settings-rail-item", { hasText: "サインイン方式" }).click();

  // 答えは倒すトグルと同じ行にある（別の面に置くと、弾かれた人が辿り着けない）。
  const methods = page.locator(".admin-panel", { hasText: "このテナントで使えるサインイン方法" });
  // デプロイの 2 件 → 自前の 1 件、の順で 1 本のリストになる。
  const rows = methods.locator(".adm-mcp-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(1).locator(".as-name")).toHaveText("Microsoft でサインイン");
  await expect(rows.nth(1).locator("code")).toHaveText("entra");
  // issuer は super_admin にしか返らない（§61.17.9 ①）。返ったなら行に出す。
  await expect(rows.nth(1).locator(".as-repo")).toHaveText(IDP.issuer);
  await expect(rows.nth(1).locator(".idp-state")).toHaveText("デプロイ共通");
  // 自前の行は最後（承認前なので off）。デプロイの方式には編集も削除も無い。
  await expect(rows.nth(2).locator(".as-name")).toHaveText("entra");
  await expect(rows.nth(0).locator(".allow-acts")).toHaveCount(0);
});

// 「ボタンに出す」を倒すと、その方式はこのテナントのログイン画面から消えるが**受け入れは
// 続く**（docs/log/61 §61.17.6・P7-1 で素の /login も既定テナントのページになった）。
// 「隠した＝もう使えない」と読む人が居るので、倒したその場でそう書く。★ 自由入力の CSV を
// 埋めるのではなく行のトグルを倒す — 何が起きたかは、送った 2 本の CSV で固定する。
test("デプロイ管理者: ボタンに出すのを倒すと、受け入れは続くとその場で読める", async ({ page }) => {
  // 規則の 4 列は PUT で丸ごと置き換わる。押した結果を読み直せるように、モックも
  // 一度きりの JSON ではなく状態として持つ（onChanged → GET /api/admin/tenants）。
  const tenant = { ...TENANT, allowed_providers: "", hidden_providers: "" };
  let saved: unknown = null;
  const ACTIVE_IDP = { ...IDP, status: "active", usable: true };
  await page.route("**/api/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    if (req.method() === "PUT" && p === "/api/admin/tenants/acme/login") {
      saved = req.postDataJSON();
      Object.assign(tenant, saved);
      return route.fulfill({ json: {} });
    }
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "root", email: "root@example.com" } });
    if (p === "/api/tenants") return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: true } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [{ ...tenant }], super_admin: true } });
    // active な自前の行が 1 つある＝この URL を配れば実際に入れる（＝URL を出す条件）。
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [ACTIVE_IDP] } });
    if (p === "/api/admin/tenants/acme/members") return route.fulfill({ json: { members: MEMBERS } });
    if (p === "/api/admin/idp") return route.fulfill({ json: { providers: [ACTIVE_IDP] } });
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
  await page.locator(".settings-rail-item", { hasText: "サインイン方式" }).click();

  const methods = page.locator(".admin-panel", { hasText: "このテナントで使えるサインイン方法" });
  const note = methods.locator(".admin-hint", { hasText: "受け入れは続きます" });
  // 何も隠していないうちは出さない（読む理由が無いヒントは、他のヒントを薄める）。
  await expect(note).toHaveCount(0);
  // 配るべき URL は、その上で誰かが実際に入れるようになって初めて出る。
  await expect(methods.locator("code", { hasText: "/login/acme" })).toHaveCount(1);

  const google = methods.locator(".adm-mcp-row").first();
  const show = google.locator(".idp-flag", { hasText: "ボタンに出す" }).locator("input");
  await expect(show).toBeChecked();
  // ★ uncheck() ではなく click()。制御コンポーネントなので checked は PUT →
  // 読み直しの後でしか変わらず、uncheck() の「押した直後に状態が変わったか」の
  // 検査に間に合わない（実際には変わるのに "did not change its state" で落ちる）。
  await show.click();
  // 送るのは CSV 2 本 + ドメイン 2 列（この面が持っていない列も読んだ値をそのまま返す
  // ＝送らないと空で上書きされる）。「全部 ON なら空」なので allowed は空のまま。
  await expect.poll(() => saved).toEqual({
    allowed_providers: "",
    hidden_providers: "google",
    auto_join_domains: TENANT.auto_join_domains,
    allowed_domains: "",
  });
  await expect(show).not.toBeChecked();
  await expect(note).toHaveCount(1);
  // 受け入れは続く＝「受け入れる」は倒れていない。
  await expect(google.locator(".idp-flag", { hasText: "受け入れる" }).locator("input")).toBeChecked();
});
