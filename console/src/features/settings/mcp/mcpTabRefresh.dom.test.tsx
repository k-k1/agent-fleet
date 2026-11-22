// "Refresh" of the tenant-distributed set: a refusal and a rejected request show the failure toast
// and give the button back; a good answer replaces the list.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const toast = vi.fn();
const apiJSON = vi.fn();
const registry = { servers: [{ id: "t1", name: "tenant-one", scope: "tenant" }], tenantFetchedAt: 1700000000 };
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: async () => registry,
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

const { McpTab } = await import("./McpTab.tsx");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { setLocale } = await import("../../../lib/i18n/index.ts");

let root: Root, host: HTMLDivElement;
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  setLocale("en");
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

async function mountAndRefresh() {
  await act(async () => root.render(<McpTab />));
  await act(async () => {});
  const btn = host.querySelector<HTMLButtonElement>(".mcp-tenant-bar button")!;
  await act(async () => btn.click());
  return btn;
}

describe("McpTab tenant refresh", () => {
  it("toasts the failure and re-enables the button when the request rejects", async () => {
    apiJSON.mockRejectedValueOnce(new Error("network down"));
    const btn = await mountAndRefresh();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("network down"));
    expect(btn.disabled).toBe(false);
  });
  it("toasts a refusal body", async () => {
    apiJSON.mockResolvedValueOnce({ error: { message: "nope" } });
    await mountAndRefresh();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("nope"));
  });
  it("leaves the list alone and raises no toast on a JSON null body (the old failure path threw)", async () => {
    apiJSON.mockResolvedValueOnce(null);
    const unhandled: unknown[] = [];
    const on = (e: unknown) => unhandled.push(e);
    process.on("unhandledRejection", on);
    const btn = await mountAndRefresh();
    process.off("unhandledRejection", on);
    expect(toast).not.toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
    expect(host.textContent).toContain("tenant-one");
  });
});
