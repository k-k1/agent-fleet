// One happy path for the Console UI E2E (L3): open the Console in a browser, open a session
// from the left pane, and check that keystrokes into xterm reach bash in the real container.
// xterm.js draws to canvas/WebGL, so the characters cannot be read from the DOM; instead of
// asserting on terminal content we observe "keystroke -> a file appears in the container"
// through CP's fs API, which exercises browser -> WS relay -> PTY -> bash -> fs for real.
import { test, expect } from "@playwright/test";

const base = process.env.E2E_CP_BASE || "";

test.skip(!base, "skipped: docker / the image / console-dist is missing (on CI, E2E_REQUIRE=1 makes setup fail instead)");

test("Console -> open a session -> keystrokes reach the container", async ({ page, request }) => {
  await page.goto(base + "/");

  // The shell session global-setup created through the API (under home, so no repo) shows up
  // in the left pane's "other sessions" group as a SessionRow (.sess-btn).
  const row = page.locator(".sess-btn", { hasText: process.env.E2E_SESSION_TITLE || "e2e-ui" });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.click();

  // The terminal pane opens and xterm mounts (.xterm appears under .terminal).
  const term = page.locator(".termview .terminal .xterm").first();
  await expect(term).toBeVisible({ timeout: 30_000 });
  await term.click(); // focus xterm

  // Completion of the WS connect and PTY attach is not observable from the DOM, so instead of a
  // fixed sleep the retry unit is "keystroke -> a file appears in the container": keystrokes
  // dropped before attach are simply typed again, and re-running echo is idempotent. The
  // verdict comes from a fact on the container side (the fs API is relative to home).
  const marker = `ui-marker-${Date.now()}.txt`; // unique to this test, shared with no other
  const nonce = `ui-ok-${Date.now()}`;
  await expect
    .poll(
      async () => {
        await page.keyboard.type(`echo ${nonce} > ${marker}`, { delay: 30 });
        await page.keyboard.press("Enter");
        const res = await request.get(`${base}/api/fs/file?path=${marker}`);
        if (!res.ok()) return "";
        const j: any = await res.json().catch(() => ({}));
        return j.content || "";
      },
      { timeout: 60_000, intervals: [1000] },
    )
    .toContain(nonce);
});

test("edit text -> save by keyboard and by button -> CAS conflict shown while keeping mine", async ({ page, request }) => {
  // Create a file with a name unique to this test in the real container through the API, so it
  // depends on no earlier test's output and holds when run alone or reordered.
  const marker = `ui-edit-${Date.now()}.txt`;
  const created = await request.post(`${base}/api/fs/newfile?path=${marker}`);
  expect(created.ok()).toBeTruthy();

  await page.goto(base + "/");

  // Open the text file created above from the recursive search over the home scope.
  const files = page.locator(".ui-section", { has: page.locator(".ui-section-title", { hasText: /ファイル|Files/ }) });
  const toggle = files.locator(".ui-section-toggle");
  if ((await toggle.getAttribute("aria-expanded")) !== "true") await toggle.click();
  await files.getByRole("button", { name: /home から検索|Search from home/ }).click();
  await files.locator(".proj-filter input").fill(marker);
  const row = files.locator(`.fsrow[data-path="${marker}"]`);
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.click();

  const tabs = page.getByRole("tablist", { name: /ファイル表示モード|File display mode/ });
  const viewTab = tabs.getByRole("tab", { name: /表示|View/ });
  const editTab = tabs.getByRole("tab", { name: /編集|Edit/ });
  await expect(viewTab).toHaveAttribute("aria-selected", "true");
  await editTab.click();
  await expect(editTab).toHaveAttribute("aria-selected", "true");
  const cm = page.locator(".file-editor-cm .cm-content");
  await expect(cm).toBeFocused();

  // Check Ctrl/Cmd+S and the always-present Save button through the same snapshot flow.
  const first = `keyboard-save-${Date.now()}\n`;
  await page.keyboard.press("Control+A");
  await page.keyboard.type(first);
  await page.keyboard.press("Control+S");
  // Under an English locale saved (just saved) and clean (already saved) both render as
  // "Saved" and cannot be told apart, so the config pins ja-JP and we match the Japanese
  // wording anchored at the start only: the status element can append an external-change note,
  // so no $ anchor.
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/^保存しました/);
  await expect
    .poll(async () => {
      const res = await request.get(`${base}/api/fs/file?path=${marker}`);
      if (!res.ok()) return "";
      const j: any = await res.json().catch(() => ({}));
      return j.content || "";
    })
    .toBe(first);

  const second = `button-save-${Date.now()}\n`;
  await page.keyboard.press("Control+A");
  await page.keyboard.type(second);
  // exact does not apply to a RegExp name (Playwright only honours it for string names), so
  // anchor the pattern for a full match.
  await page.locator(".fileview").getByRole("button", { name: /^保存$|^Save$/ }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/^保存しました/);

  // Slip an external writer in so the PUT from the stale base gets a 409, then check the
  // mine/remote diff and the resolution controls.
  const beforeConflict: any = await (await request.get(`${base}/api/fs/file?path=${marker}`)).json();
  const mine = `mine-${Date.now()}\n`;
  await cm.click();
  await page.keyboard.press("Control+A");
  await page.keyboard.type(mine);
  const remote = `remote-${Date.now()}\n`;
  const external = await request.put(`${base}/api/fs/file`, {
    headers: { "content-type": "application/json" },
    data: {
      path: marker,
      content: remote,
      baseDiskRevision: beforeConflict.revision,
    },
  });
  expect(external.status()).toBe(200);
  await page.keyboard.press("Control+S");
  const conflict = page.getByRole("alert", { name: /リビジョン競合|Revision conflict/ });
  await expect(conflict).toBeVisible();
  await expect(conflict).toContainText(mine.trim());
  await expect(conflict).toContainText(remote.trim());
  await conflict.getByRole("button", { name: /remoteを採用|Adopt remote/ }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/^保存済み/);
  await expect(cm).toContainText(remote.trim());
});
