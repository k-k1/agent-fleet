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

describe("tenant settings modal — last-opened section persistence (#1100)", () => {
  it("first-ever open (nothing stored) defaults to sign-in", () => {
    store.useSettingsUI.getState().openTenantSettings();
    expect(store.useSettingsUI.getState().tenantSection).toBe("signin");
  });

  it("restores the last-opened section on a plain open", () => {
    store.rememberTenantSection("members");
    store.useSettingsUI.getState().openTenantSettings();
    expect(store.useSettingsUI.getState().tenantSection).toBe("members");
  });

  it("an explicit requested section wins over the remembered one (deep-link)", () => {
    store.rememberTenantSection("members");
    store.useSettingsUI.getState().openTenantSettings("rules");
    expect(store.useSettingsUI.getState().tenantSection).toBe("rules");
  });

  it("remembers the picked tenant", () => {
    expect(store.lastTenantSlug()).toBe("");
    store.rememberTenantSlug("acme");
    expect(store.lastTenantSlug()).toBe("acme");
  });
});

describe("admin modal — where it was left (#1100)", () => {
  it("defaults to the tenant list with nothing stored", () => {
    expect(store.lastAdminPlace()).toEqual({ root: "tenants", scope: null, scopeSection: "limits" });
  });

  it("round-trips root, open tenant and the section inside it", () => {
    store.rememberAdminPlace({ root: "egress", scope: "acme", scopeSection: "members" });
    expect(store.lastAdminPlace()).toEqual({ root: "egress", scope: "acme", scopeSection: "members" });
  });

  it("falls back to the defaults for a corrupt or partial value", () => {
    values.set("af-admin-place", "{not json");
    expect(store.lastAdminPlace()).toEqual({ root: "tenants", scope: null, scopeSection: "limits" });
    values.set("af-admin-place", JSON.stringify({ scope: 3, root: "" }));
    expect(store.lastAdminPlace()).toEqual({ root: "tenants", scope: null, scopeSection: "limits" });
  });
});
