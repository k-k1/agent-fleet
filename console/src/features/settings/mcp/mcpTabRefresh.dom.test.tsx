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

async function mount() {
  await act(async () => root.render(<McpTab />));
  await act(async () => {});
  return host.querySelector<HTMLButtonElement>(".mcp-tenant-bar button")!;
}
async function mountAndRefresh() {
  const btn = await mount();
  await act(async () => btn.click());
  return btn;
}

describe("McpTab tenant refresh", () => {
  it("disables the button while the request is out, then toasts the failure and re-enables it when it rejects", async () => {
    let reject!: (e: Error) => void;
    apiJSON.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
    const btn = await mount();
    await act(async () => btn.click());
    expect(btn.disabled).toBe(true);
    await act(async () => reject(new Error("network down")));
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("network down"));
    expect(btn.disabled).toBe(false);
    expect(host.textContent).toContain("tenant-one");
  });
  it("toasts a refusal body", async () => {
    apiJSON.mockResolvedValueOnce({ error: { message: "nope" } });
    await mountAndRefresh();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("nope"));
  });
  it("raises the old TypeError (no toast, list kept, button back) on a JSON null body", async () => {
    apiJSON.mockResolvedValueOnce(null);
    const unhandled: unknown[] = [];
    const on = (e: unknown) => unhandled.push(e);
    process.on("unhandledRejection", on);
    let btn!: HTMLButtonElement;
    try {
      btn = await mountAndRefresh();
      await new Promise((r) => setTimeout(r, 20));
    } finally {
      process.off("unhandledRejection", on);
    }
    expect(unhandled).toHaveLength(1);
    expect(unhandled[0]).toBeInstanceOf(TypeError);
    expect(toast).not.toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
    expect(host.textContent).toContain("tenant-one");
  });
});
