import { defineConfig } from "@playwright/test";

// Console UI E2E (L3). global-setup starts the CP and the Workspace container and passes the
// base URL to the tests through E2E_CP_BASE (unset = a prerequisite is missing, so the tests
// skip). UI E2E is flaky-prone, hence one retry on CI and traces kept only on failure.
export default defineConfig({
  testDir: "./tests",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  retries: process.env.CI ? 1 : 0,
  workers: 1, // serial: one CP and one workspace are shared
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  reporter: [["list"]],
  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    // Pin the UI language to Japanese (the Console picks its language from navigator.language).
    // Under an English locale editor.status.saved and clean both render as "Saved", so the
    // just-saved assertion cannot be told apart from the clean state (console.spec.ts).
    locale: "ja-JP",
    // Overridable by env var; defaults to the chromium baked into the image.
    launchOptions: { executablePath: process.env.E2E_CHROMIUM_PATH || "/usr/bin/chromium" },
  },
});
