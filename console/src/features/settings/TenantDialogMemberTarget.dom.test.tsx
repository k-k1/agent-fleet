// #2427: an admin notification about a member opens that member's detail in the real tenant
// dialog — from closed (on a phone too), while another member's detail is open, and across tenants.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  getTenant: () => "",
  getUser: () => "",
  setTenant: () => {},
  setUser: () => {},
  isTransientErr: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
const mobile = vi.fn(() => false);
vi.mock("../../lib/device.ts", async (orig) => ({ ...(await orig<object>()), mobileMatches: () => mobile() }));

import { TenantDialog } from "./TenantDialog.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useSettingsUI } from "./store.ts";

const tenant = (slug: string) => ({ slug, name: slug, allowed_providers: "", auto_join_domains: "", allowed_domains: "" });
const MEMBERS: Record<string, { user_key: string; email: string; role: string; state: string }[]> = {
  acme: [
    { user_key: "tanaka", email: "tanaka@acme.co.jp", role: "member", state: "stopped" },
    { user_key: "suzuki", email: "suzuki@acme.co.jp", role: "member", state: "stopped" },
  ],
  beta: [{ user_key: "ito", email: "ito@beta.co.jp", role: "member", state: "stopped" }],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const crumb = () => document.querySelector(".crumb.here")?.textContent ?? null;

async function mount(section = "members") {
  useSettingsUI.getState().openTenantSettings(section);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => { root!.render(<ConfirmProvider><TenantDialog /></ConfirmProvider>); });
  await flush();
}

beforeEach(() => {
  api.mockReset();
  mobile.mockReturnValue(false);
  localStorage.clear();
  api.mockImplementation((path: string) => {
    if (path === "api/admin/tenants") return Promise.resolve({ tenants: [tenant("acme"), tenant("beta")], super_admin: false });
    const m = /^api\/admin\/tenants\/([^/]+)\/members$/.exec(path);
    if (m) return Promise.resolve({ members: MEMBERS[m[1]] ?? [] });
    if (path.includes("/stats")) return Promise.resolve({ running: false });
    if (path.includes("/sessions")) return Promise.resolve({ sessions: [] });
    return Promise.resolve({});
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.getState().closeTenantSettings();
});

describe("TenantDialog opened from a member notification", () => {
  it("opens the member's detail and consumes the target", async () => {
    await mount("signin");
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "acme", userKey: "tanaka" }); });
    await flush();
    expect(crumb()).toBe("tanaka");
    expect(useSettingsUI.getState().tenantMemberTarget).toBeNull();
  });

  it("replaces the member whose detail is already open (section is already members)", async () => {
    await mount();
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "acme", userKey: "tanaka" }); });
    await flush();
    expect(crumb()).toBe("tanaka");
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "acme", userKey: "suzuki" }); });
    await flush();
    expect(crumb()).toBe("suzuki");
  });

  it("switches tenant and never pairs an old member with the new tenant", async () => {
    await mount();
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "acme", userKey: "tanaka" }); });
    await flush();
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "beta", userKey: "ito" }); });
    await flush();
    expect(crumb()).toBe("ito");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/beta/members");
  });

  it("steps past the rail on a phone that had the dialog closed", async () => {
    mobile.mockReturnValue(true);
    await mount("members"); // the remembered section is already members: no section change to ride on
    await act(async () => { useSettingsUI.getState().openTenantSettings("members", { slug: "acme", userKey: "tanaka" }); });
    await flush();
    expect(crumb()).toBe("tanaka");
    // The body is only visible once the dialog left the rail (entered); the layout class.
    expect(document.querySelector(".settings-layout.entered")).not.toBeNull();
  });
});
