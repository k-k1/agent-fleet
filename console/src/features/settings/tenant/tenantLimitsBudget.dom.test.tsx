// Saving tenant limits, checked against the slot pool (docs/log/64 §64.35, ADR 0045
// decision 25).
//
// An allocation over the pool limit used to save silently, and the overage only ever surfaced
// far from this screen: a Workspace inside its quota that will not start, or another tenant's
// slot being evicted. Three things are pinned:
//   1. Going over warns, it does not reject — the save still goes through, so a deployment
//      already over the limit is not frozen out of this screen.
//   2. A typo (a negative number) is rejected. 0 means unlimited, so a negative is not a small
//      limit, it is one nobody can satisfy.
//   3. Nothing is shown when the numbers fit. An "all good" on every save stops being read.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
const toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantLimits } from "./tenantScope.tsx";

const TENANT = { slug: "acme", name: "Acme", max_workspaces: 4 } as never;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantLimits slug="acme" tenant={TENANT} hasPool onChanged={() => {}} />);
  });
}

const text = () => host?.textContent || "";

async function save() {
  const btn = [...(host?.querySelectorAll("button") || [])].find((b) => b.className.includes("primary"));
  await act(async () => {
    btn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant limits checked against the slot pool", () => {
  it("saves an over-limit allocation and warns, rather than rejecting it", async () => {
    apiJSON.mockResolvedValue({
      tenant: "acme",
      max_workspaces: 50,
      pool_budget: { max_slots: 10, reserved_slots: 2, capacity: 8, allocated: 54, over: true },
    });
    await mount();
    await save();
    // The saved figures are shown, so this does not read as "the save failed".
    expect(text()).toContain("54");
    expect(text()).toContain("8");
    // Not an error toast.
    expect(toast).not.toHaveBeenCalled();
  });

  it("states the difference in denominators in the same place as the warning", async () => {
    apiJSON.mockResolvedValue({
      pool_budget: { max_slots: 10, reserved_slots: 2, capacity: 8, allocated: 54, over: true },
    });
    await mount();
    await save();
    // "Workspaces running at once" must not be read as "boxes that exist": a stopped
    // Workspace still holds a box while counting against no tenant's quota.
    expect(text()).toContain("必要条件であって十分条件ではありません");
  });

  it("shows nothing when it fits, because the server returns nothing", async () => {
    apiJSON.mockResolvedValue({ tenant: "acme", max_workspaces: 6 });
    await mount();
    await save();
    expect(text()).not.toContain("必要条件");
    expect(toast).not.toHaveBeenCalled();
  });

  it("uses the usual toast when the server rejects, with no warning panel", async () => {
    apiJSON.mockResolvedValue({ error: { message: "max_workspaces cannot be negative (0 = unlimited)" } });
    await mount();
    await save();
    expect(toast).toHaveBeenCalledWith("max_workspaces cannot be negative (0 = unlimited)");
    expect(text()).not.toContain("必要条件");
  });

  it("says next to the field that this is a concurrency limit, not a count of held slots", async () => {
    await mount();
    expect(text()).toContain("同時に動く数");
  });
});
