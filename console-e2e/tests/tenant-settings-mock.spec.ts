// Pin down the tenant settings modal (the screens from docs/log/61, moved out of the admin
// modal) by driving the real build (console/dist) headless. A mock test that needs neither CP
// nor Docker (same skeleton as plan-comments-mock.spec.ts).
//
// The rule it guards: what the UI reveals must never be looser than the server's permissions.
// As a tenant admin (super_admin: false):
//   - tenant settings are reachable from the account menu
//   - sign-in methods are editable but "approve and enable" is not offered (approval is
//     deployment-admin only per decision 30; CP's setStatus is what enforces it, this is only
//     the signpost)
//   - login rules are read-only (the PUT is fixed to withSuperAdmin, decision 19)
// The jsdom side (console/src/features/settings/TenantDialog.dom.test.tsx) checks the same
// conditions on the component alone; the route in from the account menu can only be checked by
// running the bundle.
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
// A tenant-defined GitHub row (docs/log/61 §61.15). The issuer is a constant filled in by the
// server; what distinguishes rows is the org (plus the domain ledger).
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

test("tenant admin: sign-in methods are editable but approval is not offered, and login rules are read-only", async ({
  page,
}) => {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev", user: "alice", email: "alice@acme.co.jp" } });
    // The role in this list decides whether the account menu offers tenant settings.
    if (p === "/api/tenants")
      return route.fulfill({ json: { tenants: [{ slug: "acme", name: "Acme", role: "tenant_admin" }], super_admin: false } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    if (p === "/api/sessions") return route.fulfill({ json: { sessions: [] } });
    // Where isSuper comes from: someone who is only a tenant admin gets super_admin: false.
    if (p === "/api/admin/tenants") return route.fulfill({ json: { tenants: [TENANT], super_admin: false } });
    if (p === "/api/admin/tenants/acme/idp") return route.fulfill({ json: { providers: [IDP] } });
    // The list is open to tenant admins too (§61.17.9 (1)), but issuer is returned only to
    // super_admin, so it is omitted here as well.
    if (p === "/api/admin/providers")
      return route.fulfill({ json: { providers: [{ id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google" }] } });
    // Fail unmocked APIs instead of swallowing them, to catch a path typo or an API change.
    return route.abort();
  });
  await page.goto(origin);

  await page.locator(".acct-btn").click();
  await page.locator(".acct-menu .acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  await expect(modal).toBeVisible();
  // Sign-in methods (the default section). Deployment-wide methods and the tenant's own rows
  // share a single list (docs/log/61 §61.17.5).
  const rows = modal.locator(".adm-mcp-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0).locator(".as-name")).toHaveText("Google でサインイン");
  await expect(rows.nth(0).locator("code")).toHaveText("google");
  await expect(rows.nth(0).locator(".idp-state")).toHaveText("デプロイ共通");
  // The tenant's own row is present: a tenant admin can manage their own IdP.
  await expect(rows.nth(1).locator(".as-name")).toHaveText("entra");
  await expect(rows.nth(1).locator(".idp-state")).toHaveText("承認待ち");
  const acts = modal.locator(".allow-acts button");
  await expect(acts.filter({ hasText: "編集" })).toHaveCount(1);
  await expect(acts.filter({ hasText: "承認して有効化" })).toHaveCount(0);
  // The two toggles are visible as state but cannot be flipped (the rules PUT is fixed to
  // withSuperAdmin), so they render as static chips rather than dead checkboxes.
  await expect(modal.locator(".idp-flags .idp-flag")).toHaveCount(2);
  await expect(modal.locator(".idp-flags input")).toHaveCount(0);

  // Login rules: the values are readable, but there is no input and no save button.
  await modal.locator(".settings-rail-item", { hasText: "ログイン規則" }).click();
  // The two method columns left this panel in P7-0 (§61.17.5): only the two domain columns
  // remain, and methods are reduced to a one-line hint pointing at the sign-in methods screen.
  await expect(modal.locator(".af-val")).toHaveCount(2);
  await expect(modal.locator(".af-val").nth(0)).toHaveText("@sales.acme.co.jp");
  await expect(modal.locator(".af-val").nth(1)).toContainText("未設定");
  await expect(modal.locator(".admin-hint", { hasText: "「サインイン方法」の面で行ごとに切り替えます" })).toHaveCount(1);
  await expect(modal.locator(".settings-content input")).toHaveCount(0);
  await expect(modal.locator(".settings-content .admin-actions")).toHaveCount(0);
});

// Closing the admin modal to super_admin only must not cost a tenant_admin any screen; check
// that by running the bundle. The jsdom tests cover components alone, so what the account menu
// offers and whether the moved screens actually render can only be seen here.
test("tenant admin: Admin disappears, and members and operations are reachable from tenant settings", async ({ page }) => {
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
  // Admin is a deployment-wide screen (CP fixes it to withSuperAdmin). Do not offer an entry
  // point that leads to a 403.
  await expect(menu.locator(".acct-item", { hasText: "管理" })).toHaveCount(0);
  await menu.locator(".acct-item", { hasText: "テナント設定" }).click();

  const modal = page.locator(".tenant-modal");
  // Members: list -> detail -> back. The levels stack inside the body; the rail stays on
  // members.
  await modal.locator(".settings-rail-item", { hasText: "メンバー" }).first().click();
  await expect(modal.locator(".member-row")).toHaveCount(2);
  await modal.locator(".member-row").first().click();
  await expect(modal.locator(".member-detail")).toBeVisible();
  // The whole offboarding set belongs to tenant_admin (docs/log/61 §61.10.6, decision 26).
  await expect(modal.locator(".member-detail button", { hasText: "メンバーを外す" })).toHaveCount(1);
  await expect(modal.locator(".member-detail button", { hasText: "home を掃除" })).toHaveCount(1);
  // Granting tenant_admin belongs to the deployment admin, so it is not offered here.
  await expect(modal.locator(".member-detail button", { hasText: "テナント管理者にする" })).toHaveCount(0);
  await modal.locator(".tenant-drill .admin-back").click();
  await expect(modal.locator(".member-detail")).toHaveCount(0);
  await expect(modal.locator(".member-row")).toHaveCount(2);

  // Operations: this screen never spans tenants, so no tenant picker is shown.
  await modal.locator(".settings-rail-item", { hasText: "セッション" }).click();
  await expect(modal.locator(".adm-session")).toHaveCount(1);
  await expect(modal.locator(".usage-toolbar select")).toHaveCount(0);
});

// Approval can be issued straight from a register row; the request URL is built from that row's
// tenant_slug.
test("deployment admin: can approve and enable from a row in the register", async ({ page }) => {
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
  // The admin modal is a left rail plus a body, landing on the tenant list; the register is one
  // rail item.
  await page.locator(".settings-rail-item", { hasText: "サインイン方法の登録簿" }).click();

  const register = page.locator(".admin-panel", { hasText: "テナント定義のサインイン方法" });
  await expect(register).toBeVisible();
  await register.locator("button", { hasText: "承認して有効化" }).click();

  expect(approved).toEqual({ path: "/api/admin/tenants/acme/idp/idp1/status", body: { status: "active" } });
  // Re-read after the click: an approved row turns into a suspend action, and the register does
  // not go empty.
  await expect(register.locator("button", { hasText: "停止する" })).toHaveCount(1);
  await expect(register.locator("button", { hasText: "承認して有効化" })).toHaveCount(0);
});

// The kind decides which fields are asked for (docs/log/61 §61.15). Showing issuer / tid / trust
// method on a GitHub row presents fields that cannot be filled and ends in a 400 on save, while
// omitting the org leaves the required field missing. The identity source in the list is the org
// too, because github.com is the same for every tenant and distinguishes nothing. Switching the
// kind back must not carry the issuer over: that would allow an OIDC row with issuer
// https://github.com, which saves but does not work.
test("tenant admin: a GitHub row asks for the org and shows no issuer / tid / trust method", async ({ page }) => {
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
  // Identity source: the issuer for OIDC, the org for GitHub.
  await expect(rows.nth(0).locator(".as-repo")).toHaveText(IDP.issuer);
  await expect(rows.nth(1).locator(".as-name")).toHaveText("github");
  await expect(rows.nth(1).locator(".as-repo")).toHaveText("GitHub: acme-sub");

  // Editing the github row swaps the set of fields.
  await rows.nth(1).locator("button", { hasText: "編集" }).click();
  const form = modal.locator(".adm-mcp-form");
  const labels = form.locator(".ssm-fld > label");
  await expect(labels.filter({ hasText: "許可する GitHub 組織" })).toHaveCount(1);
  await expect(form.locator(".ssm-fld input").nth(1)).toHaveValue("acme-sub");
  await expect(labels.filter({ hasText: "issuer" })).toHaveCount(0);
  await expect(labels.filter({ hasText: "email の信頼方法" })).toHaveCount(0);
  await expect(labels.filter({ hasText: "許可する Entra テナント" })).toHaveCount(0);
  // GitHub has no same-account claim either (docs/log/61 §61.15.11): its subject is the same
  // numeric id under every OAuth App, so no second key is needed.
  await expect(labels.filter({ hasText: "同一アカウントの見分け方" })).toHaveCount(0);
  // The domain stays required for GitHub too (one domain, one tenant in the ledger, §61.15.3).
  await expect(labels.filter({ hasText: "受け入れるメールドメイン" })).toHaveCount(1);

  // Switching the kind back to an own IdP brings the issuer field back, without carrying
  // github.com over.
  await form.locator("select").first().selectOption("oidc");
  await expect(labels.filter({ hasText: "許可する GitHub 組織" })).toHaveCount(0);
  // Present for OIDC, and as a select rather than free text: only names CP allows may be
  // chosen, because letting someone type e.g. email would let a method sharing the same issuer
  // reach existing accounts.
  const linkClaim = form.locator(".ssm-fld", { hasText: "同一アカウントの見分け方" }).locator("select");
  await expect(linkClaim).toHaveCount(1);
  await expect(linkClaim.locator("option")).toHaveText(["既定（sub で見分ける）", "oid"]);
  const issuer = form.locator(".ssm-fld", { hasText: "issuer（発行者 URL）" }).locator("input");
  await expect(issuer).toHaveValue("");
});

// The set of usable sign-in methods used to be a free-text CSV whose valid values existed only
// in env, so a typo simply came back as 400 unknown_provider from CP. In P7-0 that field went
// away and the deployment-wide methods returned by GET /api/admin/providers are listed as rows
// instead (docs/log/61 §61.17.5). This runs the bundle because where they live (the sign-in
// methods screen) and how they interleave with the tenant's own rows in one list cannot be
// checked on the component alone.
test("deployment admin: deployment-wide methods appear in the tenant's list with their ids", async ({ page }) => {
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
    // No secrets here: CP's login_provider_api.go returns only id, display name and issuer.
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
  // Opening a tenant replaces the whole rail, landing on limits; methods sit in the login group.
  await page.locator(".settings-rail-item", { hasText: "サインイン方式" }).click();

  // The answer sits on the same row as the toggle being flipped; on another screen, someone who
  // was refused would never reach it.
  const methods = page.locator(".admin-panel", { hasText: "このテナントで使えるサインイン方法" });
  // One list: the two deployment-wide methods first, then the tenant's own one.
  const rows = methods.locator(".adm-mcp-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(1).locator(".as-name")).toHaveText("Microsoft でサインイン");
  await expect(rows.nth(1).locator("code")).toHaveText("entra");
  // issuer is returned only to super_admin (§61.17.9 (1)); when it is, show it on the row.
  await expect(rows.nth(1).locator(".as-repo")).toHaveText(IDP.issuer);
  await expect(rows.nth(1).locator(".idp-state")).toHaveText("デプロイ共通");
  // The tenant's own row comes last (off, since it is not yet approved). Deployment-wide
  // methods offer neither edit nor delete.
  await expect(rows.nth(2).locator(".as-name")).toHaveText("entra");
  await expect(rows.nth(0).locator(".allow-acts")).toHaveCount(0);
});

// Turning off "show as a button" removes the method from this tenant's login screen but keeps
// it accepted (docs/log/61 §61.17.6; since P7-1 a bare /login is the default tenant's page too).
// People read "hidden" as "no longer usable", so say otherwise right where the toggle is. The
// action is flipping a row toggle rather than filling a free-text CSV, and what it did is
// pinned by the two CSVs that get sent.
test("deployment admin: turning off the button still reads, right there, that sign-in is still accepted", async ({ page }) => {
  // The PUT replaces all four rule columns at once. So the result of the click can be re-read,
  // the mock holds state rather than a one-shot JSON (onChanged -> GET /api/admin/tenants).
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
    // One active own row means handing out this URL actually lets people in, which is the
    // condition for showing the URL at all.
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
  // Not shown while nothing is hidden: a hint with no reason to read it dilutes the others.
  await expect(note).toHaveCount(0);
  // The URL to hand out appears only once someone can actually sign in through it.
  await expect(methods.locator("code", { hasText: "/login/acme" })).toHaveCount(1);

  const google = methods.locator(".adm-mcp-row").first();
  const show = google.locator(".idp-flag", { hasText: "ボタンに出す" }).locator("input");
  await expect(show).toBeChecked();
  // click(), not uncheck(). This is a controlled component, so checked only changes after the
  // PUT and the re-read, too late for uncheck()'s "did the state change right after the click"
  // check: it does change, but uncheck() fails with "did not change its state".
  await show.click();
  // What goes out is two CSVs plus the two domain columns: columns this screen does not own are
  // echoed back as read, because omitting them overwrites them with empty. "all on" is encoded
  // as empty, so allowed stays empty.
  await expect.poll(() => saved).toEqual({
    allowed_providers: "",
    hidden_providers: "google",
    auto_join_domains: TENANT.auto_join_domains,
    allowed_domains: "",
  });
  await expect(show).not.toBeChecked();
  await expect(note).toHaveCount(1);
  // Sign-in is still accepted, i.e. the accept toggle was not flipped.
  await expect(google.locator(".idp-flag", { hasText: "受け入れる" }).locator("input")).toBeChecked();
});
