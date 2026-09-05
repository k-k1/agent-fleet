#!/usr/bin/env node

// ec2-pool-shots.mjs — a one-off tool for eyeballing the operator-facing screens (Admin > Slots,
// and the Workspace destroy confirmation) against real data. docs/log/64 §64.18.6 / §64.20.
//
// This is not an ordinary E2E: it drives a real EC2 slot pool running in the sandbox, and the
// shots exist to show what an operator can read, not to pass or fail. Hence scripts/ rather
// than console-e2e/tests (same treatment as chromium-attach-p0.mjs).
//
//   node console-e2e/scripts/ec2-pool-shots.mjs http://127.0.0.1:8899 /tmp/shots
//
// Preconditions: that CP runs with AUTH=proxy and EMAIL below is in SUPER_ADMIN_EMAILS. The
// slots tab is shown only to super_admin (and only on this runtime). playwright is not a
// console-e2e dependency; this just reads the parent clone's node_modules.
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
    // AUTH=proxy trusts this header; it stands in for oauth2-proxy.
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

  // The admin screen lives in the account menu, as a dialog separate from the settings modal.
  await page.getByText(EMAIL).first().click();
  await page.waitForTimeout(600);
  await shot(page, "01-account-menu");
  await page.getByText(/^\s*(Admin|管理)\s*$/).first().click();
  await page.waitForTimeout(3000);
  await shot(page, "02-admin");

  // Admin > Slots, shown only when AF_RUNTIME=ecs-ec2.
  const slotTab = page.getByRole("button", { name: /スロット|Slots/ }).first();
  if (await slotTab.count()) {
    await slotTab.click();
    await page.waitForTimeout(8000); // slow: PoolStatus queries AWS on every load
    await shot(page, "03-pool");
    console.log("--- pool tab text ---");
    console.log(await page.locator(".admin").first().innerText());
  } else {
    console.log("!! no slots tab (not super_admin, or the runtime is not ecs-ec2)");
    console.log(JSON.stringify(await page.locator("button").allTextContents()));
  }

  // Tenant detail — the hibernation settings only appear on this runtime.
  await page.getByRole("button", { name: /Tenants|テナント/ }).first().click();
  await page.waitForTimeout(1500);
  await page.getByText(/^Default$/).first().click();
  await page.waitForTimeout(2500);
  await shot(page, "04-tenant");
  const tenantText = await page.locator(".admin").first().innerText();
  for (const key of ["Hibernate unused homes", "Hibernate after", "Idle auto-stop"]) {
    console.log(`${tenantText.includes(key) ? "OK      " : "MISSING "} ${key}`);
  }

  // Removed member's detail -> destroy Workspace. The button appears only for inactive members.
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
      console.log("!! no destroy button");
      console.log(JSON.stringify(await page.locator("button").allTextContents()));
    }
  } else {
    console.log("!! leaver-example-com is not in the list (removed members are hidden)");
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
