// Console UI E2E（L3）の happy path 1 本: ブラウザで Console を開き、左ペインの
// セッションを開いて、xterm への打鍵が実コンテナの bash まで届くことを検証する。
// xterm.js は canvas/WebGL 描画で DOM から文字が読めないため、ターミナル内容の
// 目視アサートはせず「打鍵 → コンテナ内にファイルが生まれる」を CP の fs API で
// 観測する（ブラウザ → WS 中継 → PTY → bash → fs の縦串がすべて本物）。
import { test, expect } from "@playwright/test";

const base = process.env.E2E_CP_BASE || "";

test.skip(!base, "docker / イメージ / console-dist が無いため skip（CI は E2E_REQUIRE=1 で setup が fail する）");

test("Console → セッションを開く → 打鍵がコンテナに届く", async ({ page, request }) => {
  await page.goto(base + "/");

  // global-setup が API で作った shell セッション（home 配下 = repo なし）は
  // 左ペイン「その他のセッション」に SessionRow（.sess-btn）として出る。
  const row = page.locator(".sess-btn", { hasText: process.env.E2E_SESSION_TITLE || "e2e-ui" });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.click();

  // ターミナルペインが開き xterm がマウントされる（.terminal 配下に .xterm が生える）。
  const term = page.locator(".termview .terminal .xterm").first();
  await expect(term).toBeVisible({ timeout: 30_000 });
  await term.click(); // xterm にフォーカス

  // WS 接続 & PTY attach の完了は DOM から観測できないため、固定スリープでは
  // 待たず「打鍵 → コンテナにファイルが生まれる」自体をリトライ単位にする
  // （attach 前に落ちた打鍵は次の試行で打ち直す。echo の再実行は冪等）。
  // 効果はコンテナ側の事実で判定する（fs API は home 相対）。
  const marker = `ui-marker-${Date.now()}.txt`; // このテスト自前の一意名（他テストと非共有）
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

test("Text編集 → keyboard/ボタン保存 → CAS競合をmine保持で表示", async ({ page, request }) => {
  // このテスト自前の一意名ファイルを API で実コンテナに用意する（先行テストの
  // 成果物には依存しない — 単独実行・順序入替でも成立させる）。
  const marker = `ui-edit-${Date.now()}.txt`;
  const created = await request.post(`${base}/api/fs/newfile?path=${marker}`);
  expect(created.ok()).toBeTruthy();

  await page.goto(base + "/");

  // home scopeの再帰検索から、上で作成したテキストを開く。
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

  // Ctrl/Cmd+S と常設Saveボタンを同じsnapshotフローで検証する。
  const first = `keyboard-save-${Date.now()}\n`;
  await page.keyboard.press("Control+A");
  await page.keyboard.type(first);
  await page.keyboard.press("Control+S");
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/保存しました|Saved/);
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
  await page.locator(".fileview").getByRole("button", { name: /保存|Save/, exact: true }).click();
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/保存しました|Saved/);

  // 外部writerを挟んで旧baseのPUTを409にし、mine/remote差分と解決操作を確認する。
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
  await expect(page.locator(".fileview").getByRole("status")).toContainText(/保存済み|Saved/);
  await expect(cm).toContainText(remote.trim());
});
