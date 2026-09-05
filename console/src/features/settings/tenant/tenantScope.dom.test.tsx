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
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantScopeBody } from "./tenantScope.tsx";

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
