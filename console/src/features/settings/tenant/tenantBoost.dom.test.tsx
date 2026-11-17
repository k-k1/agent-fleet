// The tenant's Boost policy panel: absent where there are no rungs, lists exactly the rungs the
// deployment declares, and saves through the tenant's own PUT.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantBoostView, declaredRungs } from "./tenantBoost.tsx";

const SIZING = {
  slot_classes: [
    { id: "standard", label: "S", arch: "x86_64", slots: [{ instance_type: "a", mem_mib: 8192 }, { instance_type: "b", mem_mib: 16384 }] },
    { id: "arm", label: "A", arch: "arm64", slots: [{ instance_type: "c", mem_mib: 8192 }] },
  ],
};
const policy = (over = {}) => ({ tenant: "acme", enabled: true, max_mem_mib: 0, monthly_hours: 20, editable: true, budget_counted: false, ...over });

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

async function mount(view: unknown) {
  api.mockImplementation((p: string) => Promise.resolve(p.endsWith("/boost") ? view : SIZING));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantBoostView slug="acme" />);
  });
  return host;
}
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset().mockResolvedValue(policy({ max_mem_mib: 16384 }));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("declaredRungs", () => {
  it("is the distinct rung memories across classes, ascending", () => {
    expect(declaredRungs({ ...SIZING, runtime: "x", cpu_effective: false, mem_meaning: "slot", disk_meaning: "home" } as never)).toEqual([8192, 16384]);
  });
});

describe("TenantBoostView", () => {
  it("renders nothing where there is nothing to boost between", async () => {
    const h = await mount(policy({ editable: false }));
    expect(h.textContent).toBe("");
  });

  it("lists the declared rungs, says the budget is not enforced, and saves the chosen rung", async () => {
    const h = await mount(policy());
    expect(h.textContent).toContain("枠は保存されますが強制されません");
    const chip = [...h.querySelectorAll("button.chip")].find((b) => b.textContent?.includes("16 GiB")) as HTMLButtonElement;
    expect(chip).toBeTruthy();
    await act(async () => chip.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/boost", "PUT", { enabled: true, max_mem_mib: 16384, monthly_hours: 20 });
  });

  it("hides the bounds while boost is off", async () => {
    const h = await mount(policy({ enabled: false }));
    expect(h.querySelectorAll("button.chip").length).toBe(0);
  });
});
