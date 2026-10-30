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

describe("settings modal — section:view requests", () => {
  it("splits memory:changes into the section and the view", () => {
    store.useSettingsUI.getState().openSettings("memory:changes");
    expect(store.useSettingsUI.getState().settingsSection).toBe("memory");
    expect(store.useSettingsUI.getState().settingsView).toBe("changes");
  });

  it("a plain section request carries no view, and closing drops a pending one", () => {
    store.useSettingsUI.getState().openSettings("memory:changes");
    store.useSettingsUI.getState().openSettings("memory");
    expect(store.useSettingsUI.getState().settingsView).toBeNull();
    store.useSettingsUI.getState().openSettings("memorysnapshots:targets");
    store.useSettingsUI.getState().closeSettings();
    expect(store.useSettingsUI.getState().settingsView).toBeNull();
  });

  it("remembers the view per section, independently", () => {
    store.rememberSectionView("memory", "settings");
    store.rememberSectionView("memorysnapshots", "targets");
    expect(store.lastSectionView("memory")).toBe("settings");
    expect(store.lastSectionView("memorysnapshots")).toBe("targets");
    expect(store.lastSectionView("git")).toBe("");
  });

  it("reads a corrupt stored value as nothing remembered", () => {
    values.set("af-settings-views", "{not json");
    expect(store.lastSectionView("memory")).toBe("");
    store.rememberSectionView("memory", "changes");
    expect(store.lastSectionView("memory")).toBe("changes");
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

describe("settings modal — scroll target", () => {
  it("carries the requested target, and a plain open or a close drops it", () => {
    const ui = store.useSettingsUI;
    ui.getState().openSettings("agents", { target: "claude" });
    expect(ui.getState().settingsTarget).toBe("claude");
    ui.getState().openSettings("agents");
    expect(ui.getState().settingsTarget).toBeNull();
    ui.getState().openSettings("agents", { target: "codex" });
    ui.getState().closeSettings();
    expect(ui.getState().settingsTarget).toBeNull();
  });
});
