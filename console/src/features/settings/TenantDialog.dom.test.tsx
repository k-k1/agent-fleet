// The tenant settings modal (docs/log/61). What it guards is that the screen never shows more
// than the permission the server holds, so only that is pinned in jsdom:
//   1. By default — a tenant admin (super_admin: false) — "approve and enable" is not rendered.
//      The flag always comes from the GET /api/admin/tenants response, the same path as
//      production: injecting isSuper from the test would leave that default path unmeasured.
//   2. Approval appears only when super_admin: true comes back (not hardcoded away).
//   3. Login rules are read-only (the PUT is fixed to withSuperAdmin, ADR0043 decision 19).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantDialog } from "./TenantDialog.tsx";
import { useSettingsUI } from "./store.ts";

const TENANT = {
  slug: "acme",
  name: "Acme",
  allowed_providers: "entra",
  auto_join_domains: "@sales.acme.co.jp",
  allowed_domains: "",
};
const IDP = {
  id: "idp1",
  name: "entra",
  issuer: "https://login.microsoftonline.com/guid/v2.0",
  client_id: "cid",
  trust: "issuer",
  allowed_domains: "@acme.co.jp",
  status: "pending",
  usable: false,
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const MEMBER = { user_key: "tanaka", email: "tanaka@acme.co.jp", role: "member", state: "running" };

function respond(superAdmin: boolean) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/tenants") {
      return Promise.resolve({ tenants: [TENANT], super_admin: superAdmin });
    }
    if (path.endsWith("/idp")) return Promise.resolve({ providers: [IDP] });
    if (path.endsWith("/members")) return Promise.resolve({ members: [MEMBER] });
    if (path.includes("/stats")) return Promise.resolve({ running: true, mem_used: 1, mem_max: 2 });
    if (path.includes("/sessions")) return Promise.resolve({ sessions: [] });
    if (path.startsWith("api/admin/sessions")) return Promise.resolve({ sessions: [] });
    if (path.startsWith("api/admin/audit")) return Promise.resolve({ audit: [] });
    return Promise.resolve({});
  });
}

async function mount(section: string) {
  useSettingsUI.getState().openTenantSettings(section);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantDialog />);
  });
  // Flush the two stages: fetch the tenants, then fetch the IdPs for that slug.
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const buttonTexts = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".allow-acts button")).map(
    (b) => b.textContent || "",
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.getState().closeTenantSettings();
});

describe("TenantDialog", () => {
  it("hides approval from a tenant admin (the default, super_admin: false)", async () => {
    respond(false);
    await mount("signin");
    expect(api).toHaveBeenCalledWith("api/admin/tenants");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/idp");
    // Edit, request-stop and delete belong to a tenant admin; only approval is withheld.
    const texts = buttonTexts();
    expect(texts.length).toBeGreaterThan(0);
    expect(texts.some((t) => t.includes("承認して有効化"))).toBe(false);
  });

  it("shows approval only when super_admin comes back", async () => {
    respond(true);
    await mount("signin");
    expect(buttonTexts().some((t) => t.includes("承認して有効化"))).toBe(true);
  });

  it("renders login rules read-only, with 'not set' readable as a value", async () => {
    respond(false);
    await mount("rules");
    const content = document.querySelector(".settings-content")!;
    // No input and no save button: the PUT is super_admin-only, so don't render something that
    // looks pressable.
    expect(content.querySelector("input")).toBeNull();
    expect(content.querySelector(".admin-actions")).toBeNull();
    const vals = Array.from(content.querySelectorAll(".af-val")).map((e) => e.textContent || "");
    // Only the two domain rows remain. The two provider rows (accepted / shown as a button) left
    // this surface for per-row toggles on the sign-in methods screen (docs/log/61 §61.17.5): a
    // read-only CSV does not answer "how do we actually sign in here".
    expect(vals).toHaveLength(2);
    expect(vals[0]).toBe("@sales.acme.co.jp");
    expect(vals[1]).toContain("未設定");
    expect(content.textContent).toContain("「サインイン方法」の面で行ごとに切り替え");
    // The tenant's own login URL also appears on the rules surface — a human hands it out
    // (decision 28).
    expect(content.textContent).toContain("login/acme");
  });

  // Network restrictions (docs/log/66) lived only in the tenant settings modal, whose entry
  // point appears solely for members holding tenant_admin, so a super_admin with no membership
  // could never reach them. This pins the item into the rail; AdminTab pins the admin-modal copy.
  it("lists network restrictions in the rail and can open them", async () => {
    respond(false);
    await mount("network");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/network");
    const rail = document.querySelector(".settings-rail, .settings-modal")!;
    expect(rail.textContent).toContain("接続元の制限");
  });

  // A super_admin entering through this modal must not get the read-only rules, or the
  // deployment administrator ends up staring at a screen saying only a deployment administrator
  // can change this. The split follows the server's super_admin flag.
  it("renders the rules editable for a super_admin", async () => {
    respond(true);
    await mount("rules");
    const content = document.querySelector(".settings-content")!;
    expect(content.querySelector("input")).not.toBeNull();
  });

  it("drills members list → detail, and back returns to the list", async () => {
    respond(false);
    await mount("members");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/members");
    const row = document.querySelector<HTMLButtonElement>(".member-row");
    expect(row).toBeTruthy();
    expect(document.querySelector(".tenant-drill")).toBeNull(); // no breadcrumb at the list level

    await act(async () => {
      row!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    // The detail level, stacked inside the body. It needs a breadcrumb back to the list: the
    // rail item stays on Members, so the rail alone cannot get back.
    expect(document.querySelector(".member-detail")).toBeTruthy();
    const crumb = document.querySelector<HTMLButtonElement>(".tenant-drill .admin-back");
    expect(crumb).toBeTruthy();
    // Removing a member and cleaning their home are tenant_admin operations
    // (docs/log/61 §61.10.6), so they are present.
    expect(document.querySelector(".member-detail")!.textContent).toContain("メンバーを外す");

    await act(async () => {
      crumb!.click();
    });
    expect(document.querySelector(".member-detail")).toBeNull();
    expect(document.querySelector(".member-row")).toBeTruthy();
  });

  it("keeps the operations surface to one tenant (no tenant picker)", async () => {
    respond(false);
    await mount("sessions");
    const content = document.querySelector(".settings-content")!;
    expect(api).toHaveBeenCalledWith("api/admin/sessions?tenant=acme");
    // This screen never spans tenants, so there is no all-tenants selector.
    expect(content.querySelector(".usage-toolbar select")).toBeNull();
  });

  it("reads MCP distribution scoped to this tenant", async () => {
    respond(false);
    await mount("mcp");
    expect(api).toHaveBeenCalledWith("api/admin/mcp-servers?tenant=acme");
    expect(document.querySelector(".settings-content .usage-toolbar select")).toBeNull();
  });
});
