// #2427: clicking an admin notice about a member opens that member in tenant settings.
import { describe, expect, it, vi } from "vitest";
import { useSettingsUI } from "../settings/store.ts";
import { openNotificationTarget, type FleetNotification } from "./store.ts";

describe("openTenantSettings with a member target", () => {
  it("records the tenant and member for the roster, and closing drops it", () => {
    useSettingsUI.getState().openTenantSettings("members", { slug: "sales", userKey: "w-acme-co-jp" });
    expect(useSettingsUI.getState().tenantSection).toBe("members");
    expect(useSettingsUI.getState().tenantMemberTarget).toEqual({ slug: "sales", userKey: "w-acme-co-jp" });
    useSettingsUI.getState().closeTenantSettings();
    expect(useSettingsUI.getState().tenantMemberTarget).toBeNull();
  });

  it("a plain open leaves no target behind", () => {
    useSettingsUI.getState().openTenantSettings("members", { slug: "sales", userKey: "x" });
    useSettingsUI.getState().openTenantSettings("members");
    expect(useSettingsUI.getState().tenantMemberTarget).toBeNull();
    vi.restoreAllMocks();
  });
});

describe("clicking a member notice", () => {
  const notice = (kind: string): FleetNotification => ({
    seq: 1, id: "e1", kind, target: { type: "member", id: "w-acme-co-jp" }, displayName: "w@acme.co.jp",
    payload: { tenantSlug: "sales", userKey: "w-acme-co-jp" }, createdAt: "2026-11-01T00:00:00Z", seen: false,
  });
  it.each(["member-home-full", "member-boot-install"])("%s opens the member's detail", async (kind) => {
    useSettingsUI.getState().closeTenantSettings();
    const r = await openNotificationTarget(notice(kind), false);
    expect(r.opened).toBe(true);
    expect(useSettingsUI.getState().tenantOpen).toBe(true);
    expect(useSettingsUI.getState().tenantMemberTarget).toEqual({ slug: "sales", userKey: "w-acme-co-jp" });
  });
});
