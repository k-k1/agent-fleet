// Exercise the file section's recursive search (home scope) against the real console build in a
// real browser with a mock backend. Same approach as editor-mock.spec.ts (serve dist statically
// and replace /api/** with route), so neither docker nor the image is needed: it fails here,
// before CI's image job, even on a machine that cannot run console.spec.ts (real CP, real
// container).
//
// Why it exists: this path once broke by blanking the whole Console the moment a query was
// typed. ProjectFiles' layout effect for sticky lineage re-ran on every render once searchMode
// was entered, and an unguarded setSticky pushed a new object each time, so React unmounted the
// root with "maximum update depth" (#185). The symptom is not "no rows" but "the app is gone",
// and console.spec.ts could only say `.fsrow` was missing while the failure screenshot was
// black. Here pageerror and the contents of #root are watched directly, so a blank app is
// reported as a blank app.
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect } from "@playwright/test";

const root = path.resolve(__dirname, "../..");
const dist = path.join(root, "console", "dist");
let server: http.Server;
let origin = "";

// Stands for a real file directly under home: outside repos, so it shows up only in home scope.
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
  // If beforeAll failed before listen, server was never created (same guard as editor-mock).
  if (!server) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

test("file search: recursive search in home scope lists rows and does not blank the root", async ({ page }) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  const searchCalls: string[] = [];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    if (p === "/api/whoami") return route.fulfill({ json: { auth_mode: "dev" } });
    if (p === "/api/tenants") return route.fulfill({ json: { tenants: [] } });
    if (p === "/api/workspace") return route.fulfill({ json: { state: "running" } });
    // Same "repos is empty" state as the real e2e workspace: nothing cloned.
    if (p === "/api/fs/tree") return route.fulfill({ json: { entries: [] } });
    if (p === "/api/fs/search") {
      searchCalls.push(url.search);
      const q = url.searchParams.get("q") || "";
      // Same semantics as the real backend (rg): home scope = empty path, substring match.
      const hit = url.searchParams.get("path") === "" && q !== "" && MARKER.includes(q);
      return route.fulfill({ json: { results: hit ? [MARKER] : [], truncated: false } });
    }
    // Abort anything but the explicitly mocked paths rather than swallowing it (same reason as
    // editor-mock: a path typo in the mock or the app, or an API change, must not stay green
    // behind an empty 200). Measured: the left pane shell still renders with every unknown API
    // aborted.
    return route.abort();
  });

  await page.goto(origin);

  const files = page.locator(".ui-section", { has: page.locator(".ui-section-title", { hasText: /ファイル|Files/ }) });
  await expect(files).toHaveCount(1);
  const toggle = files.locator(".ui-section-toggle");
  if ((await toggle.getAttribute("aria-expanded")) !== "true") await toggle.click();

  // Switch the scope to home (repos is the default). Select by the accessible name from
  // aria-label rather than a positional nth(), so it keeps matching by meaning as buttons come
  // and go.
  const homeScope = files.getByRole("button", { name: /home から検索|Search from home/ });
  await homeScope.click();
  await expect(homeScope).toHaveAttribute("aria-pressed", "true");

  await files.locator(".proj-filter input").fill(MARKER);

  // A hit row appears, i.e. the search was actually issued and the flat result rendered.
  await expect(files.locator(`.fsrow[data-path="${MARKER}"]`)).toBeVisible({ timeout: 15_000 });
  expect(searchCalls.some((s) => s.includes("path=&"))).toBeTruthy();

  // Direct guard against blanking. With only the row assertion above, an infinite loop that
  // unmounts the root also fails as "element not found", hiding that the app disappeared.
  expect(pageErrors).toEqual([]);
  expect((await page.locator("#root").innerHTML()).length).toBeGreaterThan(0);

  // Clearing the query returns to the normal tree view: entering and leaving searchMode must
  // both be stable, so a fix on the way in cannot leave the same loop on the way out.
  await files.locator(".proj-filter input").fill("");
  await expect(files.locator(".proj-filter input")).toHaveValue("");
  expect(pageErrors).toEqual([]);
  expect((await page.locator("#root").innerHTML()).length).toBeGreaterThan(0);
});
