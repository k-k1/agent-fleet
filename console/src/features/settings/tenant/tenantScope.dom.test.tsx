// Pins where the destructive "delete tenant" action may appear (docs/log/61 §61.18).
//
// Two things:
//   1. It appears only in the admin modal, i.e. the caller that passes onDeleted. The tenant
//      settings modal is a tenant's own settings screen and has no business deleting it.
//   2. It calls DELETE /api/admin/tenants/{slug} and, on success, leaves via the caller rather
//      than staying inside a tenant that no longer exists.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  // MemberView reads the deployment's home operations from the tenant store's whoami, and
  // that store reads these at import.
  getTenant: () => "",
  getUser: () => "",
  setTenant: () => {},
  setUser: () => {},
  isTransientErr: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { admin as ja } from "../../../lib/i18n/locales/ja/admin.ts";
import { TenantScopeBody, tenantScopeGroups } from "./tenantScope.tsx";

const TENANT = { slug: "sales", name: "営業部", users: 0, running: 0 };

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(onDeleted?: () => void) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TenantScopeBody
        slug="sales"
        tenant={TENANT}
        section="limits"
        isSuper
        member={null}
        onOpenMember={() => {}}
        onCloseMember={() => {}}
        onChanged={() => {}}
        onDeleted={onDeleted}
      />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const buttonWith = (text: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
    (b) => (b.textContent || "").trim() === text,
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue({});
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("tenant deletion", () => {
  it("is absent where no onDeleted is passed (the tenant settings modal)", async () => {
    await mount();
    expect(buttonWith("テナントを削除")).toBeFalsy();
  });

  it("appears at the end of the limits section in the admin modal, sends DELETE, then exits", async () => {
    const onDeleted = vi.fn();
    await mount(onDeleted);

    await act(async () => buttonWith("テナントを削除")!.click());
    const confirm = buttonWith("削除する");
    expect(confirm).toBeTruthy();
    await act(async () => confirm!.click());

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/sales", "DELETE", {});
    expect(onDeleted).toHaveBeenCalled();
  });

  it("does not exit when refused (there is still something to clear up first)", async () => {
    const onDeleted = vi.fn();
    apiJSON.mockResolvedValue({ error: { code: "tenant_not_empty", message: "remove members first" } });
    await mount(onDeleted);

    await act(async () => buttonWith("テナントを削除")!.click());
    await act(async () => buttonWith("削除する")!.click());

    expect(onDeleted).not.toHaveBeenCalled();
  });
});

// The branch naming rules (ADR 0103 decision 10) sit next to the MCP distribution, the other
// tenant list every member's Agent pulls, and the section draws the editor for this tenant.
describe("branch rules section", () => {
  it("follows the MCP distribution in the rail and loads this tenant's rules", async () => {
    const manage = tenantScopeGroups({ cost: false }).find((g) => g.key === "manage")!;
    const keys = manage.items.map(([k]) => k);
    expect(keys.indexOf("branch-rules")).toBe(keys.indexOf("mcp") + 1);

    api.mockResolvedValue({ tenant: "sales", rules: [] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <TenantScopeBody
          slug="sales"
          tenant={TENANT}
          section="branch-rules"
          isSuper={false}
          member={null}
          onOpenMember={() => {}}
          onCloseMember={() => {}}
          onChanged={() => {}}
        />,
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api).toHaveBeenCalledWith("api/admin/tenants/sales/branch-rules");
    expect(document.querySelector("textarea")).not.toBeNull();
  });
});

// Tenant settings > Model access (ADR 0138 M2 P8): next to engine access in the manage group,
// and the section loads this tenant's policy. The rail exists only in the two modals a
// tenant_admin or super_admin reaches; the CP refuses the read to anyone else, which the page
// itself pins (tenantModelAccess.dom.test.tsx).
describe("model access section", () => {
  it("follows engine access in the rail and loads this tenant's policy", async () => {
    const manage = tenantScopeGroups({ cost: false }).find((g) => g.key === "manage")!;
    const keys = manage.items.map(([k]) => k);
    expect(keys.indexOf("model-access")).toBe(keys.indexOf("engine-access") + 1);

    api.mockResolvedValue({ error: { code: "forbidden", message: "nope" } });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <TenantScopeBody
          slug="sales"
          tenant={TENANT}
          section="model-access"
          isSuper={false}
          member={null}
          onOpenMember={() => {}}
          onCloseMember={() => {}}
          onChanged={() => {}}
        />,
      );
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(api).toHaveBeenCalledWith("api/admin/tenants/sales/route-policy");
    expect(document.querySelector(".model-access [role=alert]")?.textContent).toBe("nope");
  });
});

const JA = ja as Record<string, string>;

describe("limits form field explanations (docs/CONVENTIONS.md §13)", () => {
  // Each moved explanation must hang off its own field, and the field keeps its label.
  const PAIRS: [id: string, hintKey: string][] = [
    ["ts-max-ws-mem", "admin.ws_mem_hint_1"],
    ["ts-interaction-halt", "admin.interaction_hint"],
    ["ts-image-refresh-quiet", "admin.image_refresh_hint"],
  ];

  it("keeps each explanation behind the ⓘ of its own labelled input", async () => {
    await mount();
    for (const [id, hintKey] of PAIRS) {
      const input = document.getElementById(id) as HTMLInputElement;
      expect(input, id).toBeTruthy();
      // The label is associated with the input (not with the ⓘ button) and names it.
      expect(input.labels?.length).toBe(1);
      expect(input.labels![0].textContent!.trim()).not.toBe("");
      const row = input.closest(".admin-fld")!;
      const btn = row.querySelector<HTMLButtonElement>("button.ui-field-info")!;
      const panel = document.getElementById(btn.getAttribute("aria-controls")!)!;
      expect(panel.hidden).toBe(true);
      // The panel holds this field's own explanation, not another field's.
      expect(panel.textContent).toContain(JA[hintKey].slice(0, 20));
      // Clicking the label text (it targets the input, so it is not the ⓘ) leaves the panel closed.
      await act(async () => input.labels![0].click());
      expect(panel.hidden).toBe(true);
      await act(async () => btn.click());
      expect(panel.hidden).toBe(false);
    }
  });

  it("keeps consequence warnings inline while every ⓘ is closed", async () => {
    await mount();
    const visible = (needle: string) =>
      Array.from(document.querySelectorAll<HTMLElement>("p.admin-hint")).some(
        (p) => !p.closest("[hidden]") && (p.textContent || "").includes(needle),
      );
    // PAT cap: lowering it signs older tokens out at once. Terminal log: output may hold secrets.
    expect(visible(JA["admin.pat_max_days_hint"].slice(0, 20))).toBe(true);
    expect(visible(JA["admin.pat_max_days_hint"].slice(-12))).toBe(true);
    expect(visible(JA["admin.term_log_hint"].slice(0, 20))).toBe(true);
    expect(visible("機密")).toBe(true);
  });
});
