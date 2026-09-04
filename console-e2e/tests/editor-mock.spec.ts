import { createHash } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { test, expect } from "@playwright/test";

const root = path.resolve(__dirname, "../..");
const dist = path.join(root, "console", "dist");
let server: http.Server;
let origin = "";

const revision = (content: string) =>
  "sha256:" + createHash("sha256").update(content, "utf8").digest("hex");

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
  // If beforeAll failed before listen, server was never created; an unconditional close would
  // throw a TypeError here and hide the real failure from beforeAll.
  if (!server) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

test("CodeMirror save, conflict, dirty navigation guard, ARIA", async ({ page }) => {
  let disk = "base\n";
  let unknownNext = false;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    if (url.pathname === "/api/whoami") {
      return route.fulfill({ json: { auth_mode: "dev" } });
    }
    if (url.pathname === "/api/tenants") {
      return route.fulfill({ json: { tenants: [] } });
    }
    if (url.pathname === "/api/workspace") {
      return route.fulfill({ json: { state: "running" } });
    }
    if (url.pathname === "/api/fs/file" && method === "GET") {
      return route.fulfill({
        json: {
          path: "mock.txt",
          size: Buffer.byteLength(disk),
          binary: false,
          truncated: false,
          editable: true,
          editabilityReason: null,
          content: disk,
          revision: revision(disk),
        },
      });
    }
    if (url.pathname === "/api/fs/file" && method === "PUT") {
      const body = route.request().postDataJSON() as {
        content: string;
        baseDiskRevision: string;
      };
      if (body.baseDiskRevision !== revision(disk)) {
        return route.fulfill({
          status: 409,
          json: { error: { code: "revision_conflict", message: "changed" } },
        });
      }
      disk = body.content;
      if (unknownNext) {
        unknownNext = false;
        return route.fulfill({
          status: 500,
          json: { error: { code: "write_state_unknown", message: "directory fsync failed" } },
        });
      }
      return route.fulfill({
        json: { path: "mock.txt", size: Buffer.byteLength(disk), revision: revision(disk) },
      });
    }
    // Do not swallow anything but the explicitly mocked paths with an empty 200: abort so a
    // typo in the mock or the app, or an API change, fails visibly. An unknown API only sends
    // the app down its retry path and does not affect the fileview this test observes.
    return route.abort();
  });
  await page.addInitScript(() => {
    // The key is the implementation's LKEY_NEW (console/src/layout/migrate.ts) =
    // "af.layout2.<user>.<slug>". The whoami mock above returns auth_mode:"dev" (empty user)
    // and no tenants (empty slug), so it is "af.layout2..". If it ever drifts from the
    // implementation the app falls back to a blank terminal and the fileview / tablist
    // assertions right below fail, which is how the drift is caught.
    sessionStorage.setItem("af.layout2..", JSON.stringify({
      cols: [{
        id: "c1",
        rowRatio: 1,
        panes: [{
          id: "p1",
          session: null,
          wrap: null,
          content: { kind: "file", filePath: "mock.txt" },
        }],
      }],
      colRatios: [1],
      activeId: "p1",
    }));
  });
  await page.goto(origin);

  // Pin down first that the injected layout was restored and the file pane opened at all.
  await expect(page.locator(".fileview")).toBeVisible();
  const tabs = page.getByRole("tablist", { name: /ファイル表示モード|File display mode/ });
  const edit = tabs.getByRole("tab", { name: /編集|Edit/ });
  await expect(edit).toHaveAttribute("aria-selected", "false");
  await page.setViewportSize({ width: 390, height: 844 });
  // exact does not apply to a RegExp name (Playwright only honours it for string names), so
  // anchor the pattern for a full match and avoid matching other buttons such as AI suggestions.
  await expect(page.locator(".fileview").getByRole("button", { name: /^保存$|^Save$/ })).toBeVisible();
  await page.setViewportSize({ width: 1280, height: 720 });
  await edit.click();
  await expect(edit).toHaveAttribute("aria-selected", "true");
  const cm = page.locator(".file-editor-cm .cm-content");
  await expect(cm).toBeFocused();

  await page.keyboard.press("Control+A");
  await page.keyboard.type("saved\n");
  await page.keyboard.press("Control+S");
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/保存しました|Saved/);
  expect(disk).toBe("saved\n");

  await page.keyboard.press("Control+A");
  await page.keyboard.type("button\n");
  await page.locator(".fileview").getByRole("button", { name: /^保存$|^Save$/ }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/保存しました|Saved/);
  expect(disk).toBe("button\n");

  await cm.click();
  await page.keyboard.press("Control+A");
  await page.keyboard.type("uncertain\n");
  unknownNext = true;
  await page.keyboard.press("Control+S");
  const unknown = page.getByRole("alert", { name: /保存状態を確認できません|Save state is unknown/ });
  await expect(unknown).toContainText(/耐久性|durability/);
  await unknown.getByRole("button", { name: /リスクを承認|Accept risk/ }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/リスクを承認|accepting the durability risk/);

  await cm.click();
  await page.keyboard.press("Control+A");
  await page.keyboard.type("mine\n");
  const persisted = await page.evaluate(() => {
    const values: string[] = [];
    for (const storage of [localStorage, sessionStorage]) {
      for (let i = 0; i < storage.length; i++) values.push(storage.getItem(storage.key(i)!) || "");
    }
    return values.join("\n");
  });
  expect(persisted).not.toContain("mine");
  const unloadPrevented = await page.evaluate(() => {
    const fire = () => {
      const event = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(event);
      return event.defaultPrevented;
    };
    const normal = fire();
    (window as unknown as { __afUpdating?: boolean }).__afUpdating = true;
    const whileUpdating = fire();
    delete (window as unknown as { __afUpdating?: boolean }).__afUpdating;
    return { normal, whileUpdating };
  });
  // Only the terminal's beforeunload guard (TerminalView) reads __afUpdating, the marker for a
  // version-update reload; the editor's dirty guard must keep protecting unsaved edits even
  // during an update (reloadForUpdate settles the dirty guard modal before markUpdating).
  // whileUpdating: true is what pins "the editor ignores __afUpdating".
  expect(unloadPrevented).toEqual({ normal: true, whileUpdating: true });
  disk = "remote\n";
  await page.keyboard.press("Control+S");
  const alert = page.getByRole("alert", { name: /リビジョン競合|Revision conflict/ });
  await expect(alert).toContainText("mine");
  await expect(alert).toContainText("remote");
  await alert.getByRole("button", { name: /remoteをbaseに手動マージ|Use remote as base for manual merge/ }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/未保存の変更|Unsaved changes/);

  await page.locator(".pane-close").click();
  const guard = page.getByRole("dialog", { name: /未保存の変更|Unsaved changes/ });
  await expect(guard).toBeVisible();
  await guard.getByRole("button", { name: /キャンセル|Cancel/ }).click();
  await expect(guard).toBeHidden();
  await expect(page.locator(".fileview")).toBeVisible();
  await page.locator(".pane-close").click();
  await expect(guard).toBeVisible();
  await guard.getByRole("button", { name: /破棄して続行|Discard and continue/ }).click();
  await expect(guard).toBeHidden();
  await expect(page.locator(".fileview")).toHaveCount(0);
});
