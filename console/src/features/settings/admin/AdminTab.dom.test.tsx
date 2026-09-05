// The admin modal's left rail (root ↔ tenant drill-down). What is pinned here is the
// information architecture itself: the rail has two levels, opening a tenant swaps it for that
// tenant's sections, and the exit returns to the list. Reverting to the old shape — a single row
// of mode tabs plus a breadcrumb in the body — fails this.
//
// isSuper always comes from the GET /api/admin/tenants response; injecting it from the test
// would leave the default path (quotas not editable) unmeasured.
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
// The reading dictionary fetches on module initialisation, i.e. from the import alone. Only the
// admin surface is under test here, so stub it out.
vi.mock("../../chat/ttsDict.ts", () => ({ setTenantDict: () => {} }));

import { AdminTab } from "./AdminTab.tsx";

const TENANTS = [
  { slug: "acme", name: "Acme", users: 3, running: 1, max_workspaces: 5, max_sessions: 10 },
  { slug: "beta", name: "Beta", users: 1, running: 0 },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function respond(superAdmin: boolean) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/tenants") {
      return Promise.resolve({ tenants: TENANTS, super_admin: superAdmin });
    }
    if (path === "api/admin/ec2-pool") return Promise.resolve({ runtime: "other" });
    if (path === "api/cost/profile") return Promise.resolve({ runtime: "", available: false, verified: false });
    if (path.endsWith("/members")) return Promise.resolve({ members: [] });
    if (path.endsWith("/idp")) return Promise.resolve({ providers: [] });
    return Promise.resolve({});
  });
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AdminTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rail = () => Array.from(host!.querySelectorAll(".settings-rail-item")).map((b) => b.textContent);
const byText = (sel: string, text: string) =>
  Array.from(host!.querySelectorAll(sel)).find((e) => e.textContent?.includes(text)) as HTMLElement | undefined;
const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("AdminTab rail", () => {
  it("shows three root groups (list / deployment-wide / cross-cutting) and no mode tabs", async () => {
    respond(true);
    await mount();
    expect(host!.querySelector(".admin-modes")).toBeNull();
    expect(host!.querySelectorAll(".settings-rail-group").length).toBe(3);
    const items = rail();
    expect(items).toContain("テナント一覧");
    expect(items).toContain("通信");
    expect(items).toContain("セッション");
    // Surfaces the runtime does not declare are omitted entirely (slots / cloud cost).
    expect(items).not.toContain("スロット");
    expect(items).not.toContain("クラウド費用");
  });

  it("swaps the rail for the tenant's sections on open, and the exit returns to the list", async () => {
    respond(true);
    await mount();
    expect(host!.querySelectorAll(".tenant-card").length).toBe(2);
    await click(byText(".tenant-card", "Acme"));

    // The rail moves to tenant scope: the tenant name heading and the exit appear.
    expect(host!.querySelector(".admin-scope-name")?.textContent).toContain("Acme");
    const items = rail();
    expect(items).toContain("上限・自動停止");
    expect(items).toContain("メンバー");
    expect(items).not.toContain("テナント一覧");
    // A super_admin can edit the quotas, so the save button is present.
    expect(byText(".admin-panel h4", "上限")).toBeTruthy();

    await click(host!.querySelector(".admin-rail-back") as HTMLElement);
    expect(rail()).toContain("テナント一覧");
    expect(host!.querySelector(".admin-scope-name")).toBeNull();
  });

  it("keeps quotas read-only for a non-super_admin (the tenant's numbers only)", async () => {
    respond(false);
    await mount();
    await click(byText(".tenant-card", "Acme"));
    expect(host!.querySelector(".tenant-summary")).toBeTruthy();
    expect(host!.querySelector(".admin-actions")).toBeNull();
  });
});
