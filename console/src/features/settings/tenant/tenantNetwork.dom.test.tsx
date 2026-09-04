// The tenant source-address restriction view (docs/log/66, ADR 0047). Two things are pinned:
//   1. When the deployment is misconfigured (proxy not declared) the save control is not
//      usable. A screen that merely displays "your IP" would let the visible ALB private
//      address be registered, producing a rule that lets everyone through while looking like a
//      restriction (decision 4).
//   2. A lockout refusal shows the server's wording verbatim: without the observed address an
//      administrator cannot tell a typo from a proxy problem.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantNetworkView } from "./tenantNetwork.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantNetworkView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const saveButton = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) =>
    (b.textContent || "").includes("保存"),
  );
const input = () => document.querySelector<HTMLInputElement>(".admin-fld input");

// Writing the value property of a React-controlled input does not fire onChange (React owns
// the setter). Write through the native setter, then dispatch an input event.
async function typeInto(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
  toast.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  host?.remove();
  root = null;
  host = null;
});

describe("source-address restriction", () => {
  it("does not allow editing when the proxy is not declared, and states the reason", async () => {
    api.mockResolvedValue({
      allowed_cidrs: "",
      your_ip: "10.20.10.5",
      proxy_hops: 0,
      editable: false,
      reason: "proxy_not_configured",
    });
    await mount();
    expect(input()!.disabled).toBe(true);
    expect(saveButton()!.disabled).toBe(true);
    // The point is to stop an unnoticed registration, so the reason has to be readable.
    expect(document.body.textContent).toContain("AF_TRUSTED_PROXY_HOPS");
  });

  it("shows the server's refusal verbatim, including the observed address", async () => {
    api.mockResolvedValue({ allowed_cidrs: "", your_ip: "198.51.100.7", editable: true, reason: "" });
    apiJSON.mockResolvedValue({
      error: { code: "would_lock_out", message: "your own address (198.51.100.7) is not in this list" },
    });
    await mount();
    await typeInto(input()!, "203.0.113.0/24");
    await act(async () => saveButton()!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/network", "PUT", {
      allowed_cidrs: "203.0.113.0/24",
    });
    expect(toast).toHaveBeenCalledWith("your own address (198.51.100.7) is not in this list");
  });

  it("writes the normalised result back into the field after a successful save", async () => {
    // The view reloads after saving, so the second GET returns the stored value, as a real
    // server would.
    api.mockResolvedValueOnce({ allowed_cidrs: "", your_ip: "203.0.113.9", editable: true, reason: "" });
    api.mockResolvedValue({ allowed_cidrs: "203.0.113.0/24", your_ip: "203.0.113.9", editable: true, reason: "" });
    apiJSON.mockResolvedValue({ tenant: "acme", allowed_cidrs: "203.0.113.0/24" });
    await mount();
    await typeInto(input()!, "203.0.113.9/24"); // written with the host part still present
    await act(async () => saveButton()!.click());
    // What remains is the stored value, not what was typed; otherwise the administrator keeps
    // believing the rule says something it does not.
    expect(input()!.value).toBe("203.0.113.0/24");
  });
});
