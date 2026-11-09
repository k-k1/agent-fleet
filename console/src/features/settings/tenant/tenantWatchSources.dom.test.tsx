// The tenant-wide switch for member-added watch sources (ADR 0133 §8). Pinned here:
//   0. It starts off: with the server's default the "Block" choice is the checked one.
//   1. The screen shows what the server stores, and each choice PUTs {enabled}.
//   2. A refused save leaves the shown state alone and surfaces the server's wording.
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
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantWatchSourcesView } from "./tenantWatchSources.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantWatchSourcesView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const radio = (i: number) => document.querySelectorAll<HTMLButtonElement>(".seg .seg-btn")[i];

beforeEach(() => {
  api.mockResolvedValue({ tenant: "acme", enabled: false });
  apiJSON.mockResolvedValue({ tenant: "acme", enabled: true });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant watch sources switch", () => {
  it("shows the stored state and PUTs the other choice", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/watch-sources");
    expect(radio(1).getAttribute("aria-checked")).toBe("true");
    await act(async () => {
      radio(0).click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/watch-sources", "PUT", { enabled: true });
    expect(radio(0).getAttribute("aria-checked")).toBe("true");
  });

  it("keeps the shown state and toasts when the server refuses", async () => {
    apiJSON.mockResolvedValue({ error: { message: "forbidden" } });
    await mount();
    await act(async () => {
      radio(0).click();
    });
    expect(toast).toHaveBeenCalledWith("forbidden");
    expect(radio(1).getAttribute("aria-checked")).toBe("true");
  });

  it("says so when the setting cannot be read, and offers no control", async () => {
    api.mockResolvedValue({ error: { message: "x" } });
    await mount();
    expect(document.querySelectorAll(".seg .seg-btn").length).toBe(0);
    expect(document.querySelector(".admin-hint.warn")).not.toBeNull();
  });
});
