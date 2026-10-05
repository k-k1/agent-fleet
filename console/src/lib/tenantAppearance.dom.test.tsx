// Per-tenant appearance (#1692): with the switch on, the theme and surface colors are saved per
// "<tenant>|<user>" in localStorage and re-applied when the tenant changes. They stay
// device-local: nothing here may ever reach the server ui-prefs.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

async function fresh(local: Record<string, unknown> = {}) {
  localStorage.setItem("af-display-settings", JSON.stringify(local));
  vi.resetModules();
  const s = await import("./settings.ts");
  let owner = "a|u1";
  s.setPrefsOwnerSource(() => owner);
  return { s, setOwner: (o: string) => (owner = o) };
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset().mockResolvedValue({});
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});
afterEach(() => vi.useRealTimers());

describe("per-tenant appearance", () => {
  it("is device-local and off by default", async () => {
    const { s } = await fresh();
    expect(s.isDeviceLocalSetting("appearancePerTenant")).toBe(true);
    expect(s.getSettings().appearancePerTenant).toBe(false);
    for (const k of ["theme", "mirrorTheme", "sharedTheme", "assistantTheme", "topbarColor", "leftpaneColor", "viewerColor", "chatColor", "sharedColor", "assistantColor"] as const) {
      expect(s.isDeviceLocalSetting(k)).toBe(true);
    }
    expect(s.isDeviceLocalSetting("paneLayout")).toBe(true);
    expect(s.isDeviceLocalSetting("locale")).toBe(false);
  });

  it("writes the current tenant's snapshot on change, and applies it on a switch", async () => {
    const { s, setOwner } = await fresh();
    s.setSetting("appearancePerTenant", true);
    s.setSettings({ theme: "light", topbarColor: "red" });
    setOwner("b|u1");
    s.applyTenantAppearance(); // b has no snapshot yet: keeps the look and files it
    expect(s.getSettings().theme).toBe("light");
    s.setSetting("theme", "dark");
    expect(document.documentElement.dataset.theme).toBe("dark");

    setOwner("a|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("light");
    expect(s.getSettings().topbarColor).toBe("red");
    expect(document.documentElement.dataset.theme).toBe("light");

    setOwner("b|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("dark");
    expect(s.getSettings().topbarColor).toBe("red"); // b's first snapshot carried a's colour
  });

  it("applies the snapshot at boot (a reload) without touching ui-prefs", async () => {
    const { s } = await fresh();
    s.setSetting("appearancePerTenant", true);
    s.setSetting("theme", "light");
    // Another tab on another tenant left its look in the shared settings copy.
    const shared = JSON.parse(localStorage.getItem("af-display-settings")!);
    localStorage.setItem("af-display-settings", JSON.stringify({ ...shared, theme: "dark" }));
    vi.resetModules();
    const s2 = await import("./settings.ts");
    s2.setPrefsOwnerSource(() => "a|u1");
    expect(s2.getSettings().theme).toBe("dark");
    s2.applyTenantAppearance();
    expect(s2.getSettings().theme).toBe("light");
  });

  it("never sends appearance keys in a ui-prefs PUT", async () => {
    apiMock.mockResolvedValue({ chatSize: 16 });
    const { s } = await fresh();
    await s.hydrateUIPrefs();
    s.setSettings({ appearancePerTenant: true, theme: "light", topbarColor: "red", iconSet: "seti" });
    await vi.advanceTimersByTimeAsync(2_000);
    const bodies = (apiJSONMock.mock.calls as [string, string, Record<string, unknown>][]).map((c) => c[2]);
    expect(bodies.length).toBeGreaterThan(0); // a PUT did happen, so the absence below means something
    for (const b of bodies) {
      for (const k of ["theme", "mirrorTheme", "sharedTheme", "assistantTheme", "topbarColor", "leftpaneColor", "viewerColor", "chatColor", "sharedColor", "assistantColor", "appearancePerTenant"]) {
        expect(b).not.toHaveProperty(k);
      }
    }
  });

  it("applies the snapshot once the owner resolves later (failed whoami, user change)", async () => {
    const { s, setOwner } = await fresh();
    setOwner("a|u1");
    s.setSetting("appearancePerTenant", true);
    s.setSetting("theme", "light"); // a|u1: light
    setOwner("a|u2");
    s.applyTenantAppearance();
    s.setSetting("theme", "dark"); // a|u2: dark
    setOwner("");
    s.applyTenantAppearance(); // unknown owner: nothing
    setOwner("a|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("light");
  });

  it("does not file the previous tenant's look under a new owner before App re-applies", async () => {
    const { s, setOwner } = await fresh();
    s.setSetting("appearancePerTenant", true);
    s.setSetting("theme", "light"); // a: light, all keys
    setOwner("b|u1");
    s.applyTenantAppearance();
    s.setSettings({ theme: "dark", topbarColor: "red" }); // b: dark + red
    setOwner("a|u1"); // the owner moves first ...
    s.setSetting("theme", "dark"); // ... a hotkey writes before App's effect runs
    expect(s.getSettings().topbarColor).not.toBe("red"); // a's own snapshot came in first
    setOwner("b|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().topbarColor).toBe("red"); // b's snapshot was not overwritten
  });

  it("does nothing while the switch is off", async () => {
    const { s, setOwner } = await fresh();
    s.setSetting("theme", "light");
    setOwner("b|u1");
    s.setSetting("theme", "dark");
    setOwner("a|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("dark");
    expect(Object.keys(localStorage).some((k) => k.startsWith("af-appearance-tenant:"))).toBe(false);
  });

  it("keeps the shown look when switched off, and restores snapshots when switched on again", async () => {
    const { s, setOwner } = await fresh();
    s.setSetting("appearancePerTenant", true);
    s.setSetting("theme", "light"); // a: light
    setOwner("b|u1");
    s.applyTenantAppearance();
    s.setSetting("theme", "dark"); // b: dark
    s.setSetting("appearancePerTenant", false);
    expect(s.getSettings().theme).toBe("dark");
    s.setSetting("theme", "light"); // shared look, snapshots untouched
    setOwner("a|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("light");
    s.setSetting("appearancePerTenant", true); // restores a's own snapshot (light)
    setOwner("b|u1");
    s.applyTenantAppearance();
    expect(s.getSettings().theme).toBe("dark"); // b's snapshot survived the off period
  });

  it("applying again for the same owner keeps edits made in between", async () => {
    const { s } = await fresh();
    s.setSetting("appearancePerTenant", true);
    s.applyTenantAppearance();
    s.setSetting("theme", "light");
    s.applyTenantAppearance(); // boot, per-tenant effect and identity effect may all call this
    expect(s.getSettings().theme).toBe("light");
  });

  it("does nothing while the owner is unknown", async () => {
    const { s, setOwner } = await fresh();
    setOwner("");
    s.setSetting("appearancePerTenant", true);
    s.setSetting("theme", "light");
    s.applyTenantAppearance();
    expect(Object.keys(localStorage).some((k) => k.startsWith("af-appearance-tenant:"))).toBe(false);
  });
});
