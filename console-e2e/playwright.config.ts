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
    // UI 言語を日本語に固定する（Console は navigator.language で言語を決める）。
    // 英語ロケールだと editor.status.saved / clean が同じ "Saved" になり
    // 「保存直後」の照合が clean 状態と区別できないため（console.spec.ts）。
    locale: "ja-JP",
    // 環境変数で上書き可能に（既定はイメージ焼き込みの chromium）。
    launchOptions: { executablePath: process.env.E2E_CHROMIUM_PATH || "/usr/bin/chromium" },
  },
});
