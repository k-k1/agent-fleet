import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// store.ts (and its import chain: layout store, api client) reads localStorage at
// module load, so stub it before importing — same pattern as repoLast.test.ts.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => values.get(k) ?? null,
  setItem: (k: string, v: string) => values.set(k, v),
  removeItem: (k: string) => values.delete(k),
});
vi.stubGlobal("window", {
  matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
  fetch: vi.fn(async () => new Response()),
  addEventListener() {},
  removeEventListener() {},
});

let store: typeof import("./store.ts");
beforeAll(async () => {
  store = await import("./store.ts");
});
beforeEach(() => values.clear());

describe("settings modal — last-opened section persistence", () => {
  it("first-ever open (nothing stored) defaults to 表示 (display)", () => {
    store.useSettingsUI.getState().openSettings();
    expect(store.useSettingsUI.getState().settingsSection).toBe("display");
  });

  it("restores the last-opened section on a plain open", () => {
    store.rememberSettingsSection("git");
    store.useSettingsUI.getState().openSettings();
    expect(store.useSettingsUI.getState().settingsSection).toBe("git");
  });

  it("an explicit requested section wins over the remembered one (deep-link)", () => {
    store.rememberSettingsSection("git");
    store.useSettingsUI.getState().openSettings("ssm");
    expect(store.useSettingsUI.getState().settingsSection).toBe("ssm");
  });

  it("keeps the legacy connections→agents alias", () => {
    store.useSettingsUI.getState().openSettings("connections");
    expect(store.useSettingsUI.getState().settingsSection).toBe("agents");
  });

  it("rememberSettingsSection writes the section to localStorage", () => {
    store.rememberSettingsSection("tokens");
    expect(values.get("af-settings-section")).toBe("tokens");
  });
});
