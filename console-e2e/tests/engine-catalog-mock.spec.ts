import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect, type Page } from "@playwright/test";

const dist = path.resolve(__dirname, "../../console/dist");
let server: http.Server;
let origin = "";

test.use({
  launchOptions: {
    executablePath: process.env.E2E_CHROMIUM_PATH || "/usr/bin/chromium",
    args: ["--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4"],
  },
});

test.beforeAll(async () => {
  expect(fs.existsSync(path.join(dist, "index.html")), "Build the Console before its browser checks").toBe(true);
  server = http.createServer((req, res) => {
    const pathname = new URL(req.url || "/", "http://local").pathname;
    const file = path.resolve(dist, pathname === "/" ? "index.html" : pathname.slice(1));
    if (!file.startsWith(dist + path.sep) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      res.writeHead(404).end();
      return;
    }
    const ext = path.extname(file);
    const contentType = ({ ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" } as Record<string, string>)[ext] || "application/octet-stream";
    res.writeHead(200, { "content-type": contentType });
    fs.createReadStream(file).pipe(res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});

test.afterAll(async () => {
  if (server) await new Promise<void>((resolve) => server.close(() => resolve()));
});

type Call = { path: string; method: string; body: Record<string, unknown> };

/** The plan's hash, as the CP would compose it. The tests assert the Console sends this back
 *  verbatim: it is the only thing that carries the destination key any more. */
const PLAN_TOKEN = "sha256:plan-harbor";

async function openCatalog(page: Page, engineKey: "image" | "llm", theme: "light" | "dark" = "dark", mode: "normal" | "no-engine" | "registered" = "normal") {
  const calls: Call[] = [];
  let started = false;
  const engines = [
    { key: "image", api: "images", provider: "comfy", base_models: ["sdxl"], file_flags: ["--vae", "--clip_l"], model_rows: mode === "registered" ? [
      { id: "harbor", enabled: false, base_model: "sdxl", description: "Harbor checkpoint", file_rows: [
        // Real sizes and a real page: the part line is drawn to put both at its right end, and a
        // fixture of 1 kB with no link cannot show whether it does.
        { s3Key: "image/checkpoints/harbor.safetensors", flag: "", bytes: 4_200_000_000, source_url: "https://example.invalid/models/harbor" },
        { s3Key: "image/vae/harbor.safetensors", flag: "--vae", bytes: 254_000_000, source_url: "https://example.invalid/models/harbor" },
      ] },
      { id: "meadow", enabled: false, base_model: "sdxl", description: "Meadow checkpoint", file_rows: [] },
    ] : [], managed: true, enabled: true, has_models: true, mode: "ondemand", state: "stopped" },
    { key: "llm", api: "chat", provider: "llamacpp", model_rows: [], managed: true, enabled: true, has_models: true, mode: "ondemand", state: "stopped" },
  ];
  /** What the bucket holds, as the ledger lists it. In the registered fixture one of harbor's
   *  two files is there and the other is not, which is the `partial` badge; elsewhere the
   *  prefix is empty until a take-in starts, and then it holds the object that press is
   *  writing — the only place progress appears now that the job history is gone. */
  const ledger = () => {
    if (mode === "registered") return [
      { key: "image/checkpoints/harbor.safetensors", bytes: 1024, role_dir: "checkpoints", placement: "ok", state: "present", declared_by: [{ model_id: "harbor", flag: "" }] },
      { key: "image/vae/harbor.safetensors", role_dir: "vae", placement: "ok", state: "missing", declared_by: [{ model_id: "harbor", flag: "--vae" }] },
    ];
    return started ? [{
      key: "image/checkpoints/harbor.safetensors", bytes: 1024, role_dir: "checkpoints", placement: "ok",
      state: "uploading", declared_by: [{ model_id: "harbor", flag: "" }], source: "civitai:101",
      job: { id: "harbor-download", state: "uploading" },
    }] : [];
  };
  await page.route("**/example-preview.svg", (route) => route.fulfill({
    contentType: "image/svg+xml",
    body: '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="240"><rect width="320" height="240" fill="#457b9d"/><circle cx="220" cy="60" r="30" fill="#ffdc88"/></svg>',
  }));
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const p = new URL(request.url()).pathname;
    const body = request.postData() ? request.postDataJSON() : {};
    calls.push({ path: p, method: request.method(), body });
    const answer = (json: unknown) => route.fulfill({ json });
    if (p === "/api/whoami") return answer({ auth_mode: "dev", email: "demo@example.com", user: "demo" });
    if (p === "/api/tenants") return answer({ tenants: [{ slug: "demo", name: "Demo", role: "tenant_admin" }], super_admin: true });
    if (p === "/api/workspace") return answer({ state: "running" });
    if (p === "/api/sessions") return answer({ sessions: [] });
    if (p === "/api/admin/engines") return answer({ engines: mode === "no-engine" ? [] : engines, super_admin: true });
    // The bucket is the ledger (ADR 0085 decision 2): one route answers what S3 holds, and both
    // the registered badge and the progress of a running ingest are read off it. `GET …/storage`
    // and the `GET …/ingest` job list are gone — answering them here would let a screen that
    // still called them pass.
    if (p.endsWith("/objects") && request.method() === "GET") return answer({ objects: ledger(), checked_at: "2026-09-15T00:00:00Z" });
    if (p.endsWith("/ingest") && request.method() === "POST") {
      // 🔴 The plan's token IS the destination decision — the key, the role, the reuse. A body
      // without one is what the CP refuses (decision 1), so this refuses it too: otherwise a
      // Console that quietly stopped sending it would still be green here.
      if (!body.plan_token) {
        return route.fulfill({ status: 400, json: { error: { code: "engine_bad_body", message: "plan_token is required" } } });
      }
      started = true;
      return answer({ id: "harbor-download", model_id: "harbor", state: "pending", action: "download" });
    }
    if (p.endsWith("/ingest/search") || p === "/api/admin/engines/search") {
      const kind = new URL(request.url()).searchParams.get("kind");
      if (p === "/api/admin/engines/search" && !["checkpoint", "gguf"].includes(kind || "")) {
        return route.fulfill({ status: 400, json: { error: { code: "engine_bad_body", message: "kind must be checkpoint or gguf" } } });
      }
      const image = p.includes("/image/") || kind === "checkpoint";
      const civitai = body.source === "civitai";
      return answer({ hits: [{
        source: civitai ? "civitai" : "hf", model_ref: civitai ? "100" : "demo/Model", ref: civitai ? "101" : "demo/Model",
        name: image ? "Harbor Image Model" : "Harbor Text Model", base_model: image ? "SDXL" : undefined,
        base_model_suggest: image ? "sdxl" : undefined, license: "apache-2.0", downloads: 123,
        published_at: "2026-09-12T12:00:00Z", updated_at: civitai ? undefined : "2026-09-13T12:00:00Z",
        preview_url: image ? `${origin}/example-preview.svg` : undefined,
      }] });
    }
    if (p.endsWith("/ingest/versions")) return answer({ versions: [{ ref: "101", name: "Version 1" }] });
    if (p.endsWith("/ingest/files")) return answer({ files: [{ name: "harbor.safetensors", bytes: 1024, sha256: "a".repeat(64) }] });
    // The resolve answers a PLAN, not a set of fields for a form to carry (decision 4): what the
    // press would download, what it would reuse, and the token the CP re-plans against.
    if (p.endsWith("/ingest/resolve")) return answer({
      source: "civitai:101", file: "harbor.safetensors", sha256: "a".repeat(64), bytes: 1024,
      license: "apache-2.0", commercial_use: "yes", can_ingest: true,
      plan: {
        plan_token: PLAN_TOKEN, id: "harbor", base_model: "sdxl", main_flag: "",
        files: [
          { flag: "", name: "harbor.safetensors", bytes: 1024, action: "download", source: "civitai:101", key: "image/checkpoints/harbor.safetensors" },
          { flag: "--vae", name: "harbor_vae.safetensors", bytes: 512, action: "reuse", source: "image/vae/harbor_vae.safetensors", key: "image/vae/harbor_vae.safetensors" },
        ],
        bytes_to_download: 1024,
      },
    });
    if (p.includes("/hf-token")) return answer({ configured: false });
    return route.abort();
  });
  await page.addInitScript(({ key, theme, registered }) => {
    localStorage.setItem("af-display-settings", JSON.stringify({ locale: "en", theme }));
    localStorage.setItem("af-tenant", "demo");
    localStorage.setItem("af.layout2.demo@example.com.demo", JSON.stringify({
      cols: [{ id: "catalog-col", rowRatio: 0.5, panes: [{ id: "catalog", session: null, content: { kind: "engineAdd", engineKey: key, lora: false, view: registered ? "registered" : "search" }, wrap: null }] }],
      colRatios: [1], activeId: "catalog",
    }));
  }, { key: engineKey, theme, registered: mode === "registered" });
  await page.goto(origin);
  await expect(page.locator(".engine-catalog-pane")).toBeVisible();
  return calls;
}

test("restored LLM pane searches HF by last modification without offering Civitai", async ({ page }) => {
  const calls = await openCatalog(page, "llm");
  const pane = page.locator(".engine-catalog-pane");
  await expect(pane.getByText("Harbor Text Model", { exact: true })).toBeVisible();
  expect(calls.filter((c) => c.path.endsWith("/ingest/search"))).toEqual(expect.arrayContaining([
    expect.objectContaining({ path: "/api/admin/engines/llm/ingest/search", body: expect.objectContaining({ source: "hf", sort: "updated" }) }),
  ]));
  await expect(pane.getByRole("button", { name: "Civitai", exact: true })).toHaveCount(0);
  await expect(pane.locator(".engines-wizard-rail")).toHaveCount(0);
});

test("without an engine the catalog still browses models and LoRAs without ingest actions", async ({ page }) => {
  const calls = await openCatalog(page, "image", "dark", "no-engine");
  const pane = page.locator(".engine-catalog-pane");
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await pane.getByRole("button", { name: "LoRAs", exact: true }).click();
  await expect.poll(() => calls.some((call) => call.path === "/api/admin/engines/search" && call.body.lora === true)).toBe(true);
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await expect(pane.getByRole("button", { name: /^Add:/ })).toHaveCount(0);
  expect(calls.filter((call) => call.path.endsWith("/ingest") && call.method === "POST")).toHaveLength(0);
});

test("registered cards read their badge off the bucket and open edits from their styled footer", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  const calls = await openCatalog(page, "image", "light", "registered");
  const pane = page.locator(".engine-catalog-pane");
  const card = pane.getByRole("listitem", { name: "harbor", exact: true });
  await expect(card).toBeVisible();
  // 🔴 The badge is the LEDGER's answer, not a row attribute: the screen reads `GET …/objects`
  // and nothing else about what exists (ADR 0085 decision 2).
  await expect(card.getByText("Storage: 1/2 files present (partial)", { exact: true })).toBeVisible();
  expect(calls.some((call) => call.path === "/api/admin/engines/image/objects")).toBe(true);
  // The same two objects, under the bucket below the rows — including the one whose bytes are
  // gone, which is a ledger fact rather than a row's.
  await expect(pane.getByRole("listitem", { name: "image/checkpoints/harbor.safetensors", exact: true })).toBeVisible();
  await expect(pane.getByRole("listitem", { name: "image/vae/harbor.safetensors", exact: true })
    .getByText("bytes absent", { exact: true })).toBeVisible();
  const edit = card.getByRole("button", { name: "Edit: harbor", exact: true });
  await expect(edit).toHaveClass(/ui-btn/);
  await page.screenshot({ path: testInfo.outputPath("registered-light.png") });
  await edit.click();
  const modal = page.getByRole("dialog");
  await expect(modal).toBeVisible();
  await expect(modal.getByRole("button", { name: "Save", exact: true })).toHaveClass(/ui-btn-primary/);
  await page.screenshot({ path: testInfo.outputPath("registered-edit-light.png") });
  await modal.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(modal).toHaveCount(0);
  await expect(edit).toBeFocused();
});

// The part line is what a person reads to answer "which file is this row, how big, where from".
// Measured rather than asserted by text: the fault it fixes was geometric — at card width the
// size and the link fell onto rows of their own, three lines below the flag they belong to.
test("a registered card's part line keeps its size and source page at the right end", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  await openCatalog(page, "image", "dark", "registered");
  const pane = page.locator(".engine-catalog-pane");
  const card = pane.getByRole("listitem", { name: "harbor", exact: true });
  const parts = card.locator(".engine-registered-parts li");
  await expect(parts).toHaveCount(2);
  const whole = parts.first();
  // 🔴 The present line carries NO chip: the card's header already says 1/2, and a badge on
  // every line is one nobody reads. The missing one keeps its own, which is the whole point.
  await expect(whole.locator(".engines-model-tag")).toHaveCount(0);
  await expect(parts.nth(1).getByText("missing", { exact: true })).toBeVisible();
  const meta = whole.locator(".engine-registered-part-meta");
  await expect(meta).toContainText("4.2 GB");
  await expect(meta.getByRole("link", { name: "Source page", exact: true })).toBeVisible();
  const flagBox = (await whole.locator(".engine-registered-part-flag").boundingBox())!;
  const metaBox = (await meta.boundingBox())!;
  const lineBox = (await whole.boundingBox())!;
  // Same line as the flag, hard against the line's right edge (8px of padding).
  expect(Math.abs(metaBox.y - flagBox.y)).toBeLessThan(flagBox.height);
  expect(metaBox.x + metaBox.width).toBeGreaterThan(lineBox.x + lineBox.width - 12);
  await page.screenshot({ path: testInfo.outputPath("registered-parts-1400.png") });
  // At phone width the card is far below the 560px container query, and the KEY is what folds —
  // under the flag, never the size or the link.
  await page.setViewportSize({ width: 390, height: 900 });
  const narrowFlag = (await whole.locator(".engine-registered-part-flag").boundingBox())!;
  const narrowMeta = (await meta.boundingBox())!;
  const narrowKey = (await whole.locator(".engine-registered-key").boundingBox())!;
  expect(Math.abs(narrowMeta.y - narrowFlag.y)).toBeLessThan(narrowFlag.height);
  expect(narrowKey.y).toBeGreaterThan(narrowFlag.y + narrowFlag.height / 2);
  await page.screenshot({ path: testInfo.outputPath("registered-parts-390.png") });
});

test("a card starts one operation and returns to browsing with its new object in the bucket", async ({ page }) => {
  const calls = await openCatalog(page, "image");
  const pane = page.locator(".engine-catalog-pane");
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await pane.getByRole("button", { name: "Add: Harbor Image Model", exact: true }).click();
  const modal = page.getByRole("dialog");
  await expect(modal).toBeVisible();
  await expect(modal.getByRole("button", { name: /^(Next|Back)$/ })).toHaveCount(0);
  await expect(modal.locator('input[name="catalog-act"]')).toHaveCount(0);
  // The plan, priced: one file to download and one the bucket already holds. Neither is a
  // question — there is no role selector, no key field and no attach/replace choice left.
  await expect(modal.getByText("no download (already held)", { exact: true })).toBeVisible();
  await expect(modal.getByText("apache-2.0", { exact: true })).toBeVisible();
  await modal.getByRole("checkbox", { name: /I accept this model/ }).check();
  await modal.getByRole("button", { name: "Take it in", exact: true }).click();
  await expect(modal).toHaveCount(0);
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await expect(pane.getByText(/^Taking it in\./)).toBeVisible();
  const starts = calls.filter((call) => call.path === "/api/admin/engines/image/ingest" && call.method === "POST");
  expect(starts).toHaveLength(1);
  // 🔴 The token instead of a destination. `s3Key` leaving the Console is the whole point of
  // decision 1 — three parties computing one key is what answered 400 on a re-ingest.
  expect(starts[0].body).toMatchObject({ id: "harbor", plan_token: PLAN_TOKEN, kind: "checkpoint", license_accepted: true });
  for (const gone of ["s3Key", "reuse_s3_key", "attach", "replace", "file_flag", "with_family_parts", "with_family_vae"]) {
    expect(starts[0].body, `${gone} must not be in the ingest body any more`).not.toHaveProperty(gone);
  }
  // Where the progress is now: the object the press is writing, on the bucket under Registered.
  // There is no job history to look at any more (decision 6).
  await pane.getByRole("tab", { name: "Registered", exact: true }).click();
  const object = pane.getByRole("listitem", { name: "image/checkpoints/harbor.safetensors", exact: true });
  await expect(object).toBeVisible();
  await expect(object.getByText("taking in", { exact: true })).toBeVisible();
  expect(calls.filter((call) => call.method === "PUT" && call.path.includes("/models/"))).toHaveLength(0);
});

for (const { width, theme } of [
  { width: 1400, theme: "dark" },
  { width: 390, theme: "dark" },
  { width: 1400, theme: "light" },
] as const) {
  test(`Image browse keeps its small right thumbnail and restores focus after the lightbox at ${width}px in ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const calls = await openCatalog(page, "image", theme);
    const pane = page.locator(".engine-catalog-pane");
    const title = pane.getByText("Harbor Image Model", { exact: true });
    await expect(title).toBeVisible();
    expect(calls.filter((c) => c.path.endsWith("/ingest/search"))).toEqual(expect.arrayContaining([
      expect.objectContaining({ body: expect.objectContaining({ source: "civitai", sort: "newest" }) }),
    ]));
    const thumbnail = pane.locator('img[src$="/example-preview.svg"]');
    await expect(thumbnail).toBeVisible();
    const box = await thumbnail.boundingBox();
    const titleBox = await title.boundingBox();
    expect(box!.width).toBeLessThanOrEqual(160);
    expect(box!.x).toBeGreaterThan(titleBox!.x);
    expect(await pane.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    const add = pane.getByRole("button", { name: "Add: Harbor Image Model", exact: true });
    await expect(add).toHaveClass(/ui-btn-primary/);
    await add.focus();
    await expect(add).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath(`catalog-${width}-${theme}.png`) });
    const trigger = thumbnail.locator("xpath=ancestor::button[1]");
    await trigger.focus();
    await page.keyboard.press("Enter");
    const lightbox = page.getByRole("dialog");
    await expect(lightbox).toBeVisible();
    await expect(lightbox.locator("img")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(lightbox).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await expect(pane.locator(".engines-wizard-rail")).toHaveCount(0);
  });
}
