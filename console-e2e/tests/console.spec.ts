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
  await page.waitForTimeout(1500); // WS 接続 & PTY attach の猶予

  const nonce = `ui-ok-${Date.now()}`;
  await page.keyboard.type(`echo ${nonce} > ui-marker.txt`, { delay: 30 });
  await page.keyboard.press("Enter");

  // 効果はコンテナ側の事実で判定する（fs API は home 相対）。
  await expect
    .poll(
      async () => {
        const res = await request.get(`${base}/api/fs/file?path=ui-marker.txt`);
        if (!res.ok()) return "";
        const j: any = await res.json().catch(() => ({}));
        return j.content || "";
      },
      { timeout: 60_000, intervals: [1000] },
    )
    .toContain(nonce);
});
