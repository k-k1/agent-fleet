#!/usr/bin/env node

// ec2-pool-shots.mjs — 運用者向けの面（管理 > スロット、および Workspace 破棄の確認）を
// **実データで**目視するための一回きりの道具。docs/log/64 §64.18.6 / §64.20。
//
// 普通の E2E ではない: 相手は sandbox に本当に立っている EC2 スロットプールで、
// 落ちるかどうかではなく「運用者が何を読めるか」を見るために撮る。だから
// console-e2e/tests ではなく scripts に置く（chromium-attach-p0.mjs と同じ扱い）。
//
//   node console-e2e/scripts/ec2-pool-shots.mjs http://127.0.0.1:8899 /tmp/shots
//
// 前提: その CP が AUTH=proxy で動いていて、SUPER_ADMIN_EMAILS に下の EMAIL が入っている
// こと。スロットタブは super_admin にしか出ない（他ランタイムではタブごと出ない）。
// playwright は console-e2e には入れていない（親クローンの node_modules を読むだけ）。
import { createRequire } from "node:module";
const require_ = createRequire(import.meta.url);
const { chromium } = require_(process.env.AF_PW_CORE || "playwright-core");
import fs from "node:fs";
import path from "node:path";

const base = process.argv[2] || "http://127.0.0.1:8899";
const outDir = process.argv[3] || "/tmp/af-pool-shots";
const EMAIL = process.env.AF_SHOT_EMAIL || "ops@example.com";

fs.mkdirSync(outDir, { recursive: true });
const shot = async (page, name) => {
  const file = path.join(outDir, `${name}.png`);
  await page.screenshot({ path: file, fullPage: true });
  console.log(`shot ${file}`);
};

const main = async () => {
  const browser = await chromium.launch({
    executablePath: process.env.AF_CHROMIUM_BIN || "/usr/bin/chromium",
    args: ["--no-sandbox"],
  });
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    // AUTH=proxy はこのヘッダを信用する。oauth2-proxy の代わり。
    extraHTTPHeaders: { "X-Forwarded-Email": EMAIL },
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  page.on("pageerror", (e) => errors.push(String(e)));

  await page.goto(base, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(3000);
  await shot(page, "00-app");

  // 管理画面はアカウントメニューの中（設定モーダルとは別のダイアログ）。
  await page.getByText(EMAIL).first().click();
  await page.waitForTimeout(600);
  await shot(page, "01-account-menu");
  await page.getByText(/^\s*(Admin|管理)\s*$/).first().click();
  await page.waitForTimeout(3000);
  await shot(page, "02-admin");

  // 管理 > スロット（AF_RUNTIME=ecs-ec2 のときだけ出る）
  const slotTab = page.getByRole("button", { name: /スロット|Slots/ }).first();
  if (await slotTab.count()) {
    await slotTab.click();
    await page.waitForTimeout(8000); // PoolStatus は毎回 AWS を引くので遅い
    await shot(page, "03-pool");
    console.log("--- pool tab text ---");
    console.log(await page.locator(".admin").first().innerText());
  } else {
    console.log("!! スロットタブが出ていない（super_admin でないか、runtime が ecs-ec2 でない）");
    console.log(JSON.stringify(await page.locator("button").allTextContents()));
  }

  // テナント詳細 — 退避の設定欄はこのランタイムでだけ出る。
  await page.getByRole("button", { name: /Tenants|テナント/ }).first().click();
  await page.waitForTimeout(1500);
  await page.getByText(/^Default$/).first().click();
  await page.waitForTimeout(2500);
  await shot(page, "04-tenant");
  const tenantText = await page.locator(".admin").first().innerText();
  for (const key of ["Hibernate unused homes", "Hibernate after", "Idle auto-stop"]) {
    console.log(`${tenantText.includes(key) ? "OK      " : "MISSING "} ${key}`);
  }

  // 外したメンバーの詳細 → Workspace 破棄。ボタンは inactive にだけ出る。
  const leaver = page.getByText(/leaver-example-com/).first();
  if (await leaver.count()) {
    await leaver.click();
    await page.waitForTimeout(2000);
    await shot(page, "05-member");
    const destroy = page.getByRole("button", { name: /Destroy|破棄/ }).first();
    if (await destroy.count()) {
      await destroy.click();
      await page.waitForTimeout(1200);
      await shot(page, "06-destroy-confirm");
      console.log("--- destroy dialog ---");
      console.log(await page.locator(".modal, [role=dialog]").last().innerText());
    } else {
      console.log("!! 破棄ボタンが出ていない");
      console.log(JSON.stringify(await page.locator("button").allTextContents()));
    }
  } else {
    console.log("!! leaver-example-com が一覧に出ていない（外したメンバーが隠れている）");
  }

  if (errors.length) {
    console.log("--- console errors ---");
    for (const e of errors.slice(0, 20)) console.log(e);
  }
  await browser.close();
};

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
