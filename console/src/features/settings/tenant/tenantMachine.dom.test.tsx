// The tenant's default machine class (docs/log/70 §70.4.3 / §70.10).
//
// Three things are pinned here:
//   1. A deployment with no classes does not render the surface at all — never a choice with
//      nothing to choose from.
//   2. What is listed is exactly the set the server returned. Re-filtering the full class list
//      here would reimplement super_admin's allow-list in the UI.
//   3. "clear the tenant default" (fall back to the deployment default) is expressed as a PUT
//      of the empty string.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantMachineView } from "./tenantMachine.tsx";

const CLASSES = [
  {
    id: "standard",
    label: "標準（Intel）",
    arch: "x86_64",
    slots: [
      { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
      { instance_type: "m7i.2xlarge", mem_mib: 32768, vcpu: 8 },
    ],
  },
  {
    id: "arm",
    label: "省コスト（Arm）",
    arch: "arm64",
    slots: [{ instance_type: "m7g.large", mem_mib: 8192, vcpu: 2 }],
  },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantMachineView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const chips = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".machine-picker .le-presets .chip"));
const chipWith = (t: string) => chips().find((b) => (b.textContent || "").trim() === t);

beforeEach(() => {
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant default machine class", () => {
  it("renders nothing at all on a deployment with no classes", async () => {
    api.mockResolvedValue({ tenant: "acme", editable: false, classes: [] });
    await mount();
    expect(document.querySelector(".machine-picker")).toBeNull();
  });

  it("lists the set the server returned as-is and marks the current default", async () => {
    api.mockResolvedValue({
      tenant: "acme",
      slot_class: "arm",
      classes: CLASSES,
      default_slot_class: "standard",
      editable: true,
    });
    await mount();
    expect(chips().map((b) => (b.textContent || "").trim())).toEqual([
      "デプロイの既定",
      "標準（Intel）",
      "省コスト（Arm）",
    ]);
    expect(chipWith("省コスト（Arm）")!.className).toContain("on");
    expect(chipWith("デプロイの既定")!.className).not.toContain("on");
    // One line per class saying what it actually buys you.
    const specs = Array.from(document.querySelectorAll(".machine-specs li")).map((e) => e.textContent || "");
    expect(specs[0]).toContain("m7i.large–m7i.2xlarge");
    expect(specs[1]).toContain("arm64");
  });

  it("saves the deployment default as the empty string, clearing the tenant default", async () => {
    api.mockResolvedValue({
      tenant: "acme",
      slot_class: "arm",
      classes: CLASSES,
      default_slot_class: "standard",
      editable: true,
    });
    apiJSON.mockResolvedValue({ tenant: "acme", slot_class: "" });
    await mount();
    await act(async () => chipWith("デプロイの既定")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/slot-class", "PUT", { slot_class: "" });
  });
});
