import { defineConfig } from "@playwright/test";

// Console UI E2E（L3）。CP・Workspace コンテナの起動は global-setup が行い、
// ベース URL は E2E_CP_BASE 経由でテストへ渡る（未設定 = 前提不足 → テスト側で skip）。
// UI E2E はフレークしやすいので CI は 1 リトライ・trace は失敗時のみ保存。
export default defineConfig({
  testDir: "./tests",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  retries: process.env.CI ? 1 : 0,
  workers: 1, // 1 CP / 1 workspace を共有するため直列
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  reporter: [["list"]],
  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
