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

async function openCatalog(page: Page, engineKey: "image" | "llm", theme: "light" | "dark" = "dark", withoutEngines = false) {
  const calls: Call[] = [];
  let started = false;
  const engines = [
    { key: "image", api: "images", provider: "comfy", base_models: ["sdxl"], file_flags: ["--vae", "--clip_l"], model_rows: [], managed: true, enabled: true, has_models: true, mode: "ondemand", state: "stopped" },
    { key: "llm", api: "chat", provider: "llamacpp", model_rows: [], managed: true, enabled: true, has_models: true, mode: "ondemand", state: "stopped" },
  ];
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
    if (p === "/api/admin/engines") return answer({ engines: withoutEngines ? [] : engines, super_admin: true });
    if (p.endsWith("/storage")) return answer({ files: [], checked_at: "2026-09-14T00:00:00Z" });
    if (p.endsWith("/models/vae-scan")) return answer({ engines });
    if (p.endsWith("/ingest") && request.method() === "POST") {
      started = true;
      return answer({ id: "harbor-download", model_id: "harbor", state: "running" });
    }
    if (p.endsWith("/ingest") && request.method() === "GET") return answer({ jobs: started
      ? [{ id: "harbor-download", model_id: "harbor", state: "running", source: "civitai:101", s3_key: "image/checkpoints/harbor.safetensors" }]
      : [] });
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
    if (p.endsWith("/ingest/resolve")) return answer({ source: "civitai:101", file: "harbor.safetensors", sha256: "a".repeat(64), bytes: 1024, license: "apache-2.0", commercial_use: "yes", can_ingest: true, base_model_suggest: "sdxl", vae_bundled: "yes" });
    if (p.includes("/hf-token")) return answer({ configured: false });
    return route.abort();
  });
  await page.addInitScript(({ key, theme }) => {
    localStorage.setItem("af-display-settings", JSON.stringify({ locale: "en", theme }));
    localStorage.setItem("af-tenant", "demo");
    localStorage.setItem("af.layout2.demo@example.com.demo", JSON.stringify({
      cols: [{ id: "catalog-col", rowRatio: 0.5, panes: [{ id: "catalog", session: null, content: { kind: "engineAdd", engineKey: key, lora: false }, wrap: null }] }],
      colRatios: [1], activeId: "catalog",
    }));
  }, { key: engineKey, theme });
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
  const calls = await openCatalog(page, "image", "dark", true);
  const pane = page.locator(".engine-catalog-pane");
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await pane.getByRole("button", { name: "LoRAs", exact: true }).click();
  await expect.poll(() => calls.some((call) => call.path === "/api/admin/engines/search" && call.body.lora === true)).toBe(true);
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await expect(pane.getByRole("button", { name: /^Add:/ })).toHaveCount(0);
  expect(calls.filter((call) => call.path.endsWith("/ingest") && call.method === "POST")).toHaveLength(0);
});

test("a card starts one operation and returns to browsing with its new job visible", async ({ page }) => {
  const calls = await openCatalog(page, "image");
  const pane = page.locator(".engine-catalog-pane");
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await pane.getByRole("button", { name: "Add: Harbor Image Model", exact: true }).click();
  const modal = page.getByRole("dialog");
  await expect(modal).toBeVisible();
  await expect(modal.getByRole("button", { name: /^(Next|Back)$/ })).toHaveCount(0);
  await expect(modal.locator('input[name="catalog-act"]')).toHaveCount(0);
  await expect(modal.getByText("apache-2.0", { exact: true })).toBeVisible();
  await modal.getByRole("checkbox", { name: /I accept this model/ }).check();
  await modal.getByRole("button", { name: "Take it in", exact: true }).click();
  await expect(modal).toHaveCount(0);
  await expect(pane.getByText("Harbor Image Model", { exact: true })).toBeVisible();
  await expect(pane.getByText("harbor", { exact: true })).toBeVisible();
  const starts = calls.filter((call) => call.path === "/api/admin/engines/image/ingest" && call.method === "POST");
  expect(starts).toHaveLength(1);
  expect(starts[0].body).toMatchObject({ id: "harbor", s3Key: "image/checkpoints/harbor.safetensors", license_accepted: true });
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
