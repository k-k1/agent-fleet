// useConnections: one shared snapshot with one request generation — latest reload wins across
// consumers, and a workspace stop clears it even when no consumer is mounted.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "zustand";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
}));
const wsStore = create<{ state: string }>(() => ({ state: "running" }));
vi.mock("../../../core/store/workspace.ts", () => ({ useWorkspaceStore: wsStore }));

const tenantStore = create<{ tenant: string; identityRev: number }>(() => ({ tenant: "a", identityRev: 0 }));
vi.mock("../../../core/store/tenant.ts", () => ({ useTenantStore: tenantStore }));

const { useConnections, resetConnectionsCache } = await import("./useConnections.ts");

let latest: ReturnType<typeof useConnections> | null = null;
function Probe() {
  latest = useConnections();
  return null;
}
const roots: Root[] = [];
async function mount() {
  const host = document.createElement("div");
  const root = createRoot(host);
  roots.push(root);
  await act(async () => root.render(<Probe />));
}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

afterEach(() => {
  roots.splice(0).forEach((r) => act(() => r.unmount()));
  resetConnectionsCache();
  wsStore.setState({ state: "running" });
  api.mockReset();
  latest = null;
});

function deferred() {
  const reqs: Array<(v: unknown) => void> = [];
  api.mockImplementation(() => new Promise((r) => reqs.push(r)));
  return reqs;
}

describe("useConnections", () => {
  it("lets the latest reload win when an older request answers last", async () => {
    const reqs = deferred();
    await mount();
    act(() => latest!.reload()); // old request, e.g. from an earlier mount
    await mount();
    act(() => latest!.reload()); // after a login: the newer state
    await act(async () => reqs[1]({ cursor: { connected: true } }));
    await act(async () => reqs[0]({ cursor: { connected: false } })); // slow, stale
    await flush();
    expect(latest!.conns).toEqual({ cursor: { connected: true } });
  });

  it("clears the snapshot when the workspace stops with no consumer mounted, and drops an old answer", async () => {
    const reqs = deferred();
    await mount();
    act(() => latest!.reload());
    await act(async () => reqs[0]({ a: 1 }));
    expect(latest!.conns).toEqual({ a: 1 });
    act(() => latest!.reload()); // request out
    roots.splice(0).forEach((r) => act(() => r.unmount())); // nobody mounted
    act(() => wsStore.setState({ state: "stopped" }));
    await act(async () => reqs[1]({ a: 2 })); // answers after the reset
    await mount();
    expect(latest!.conns).toBeNull();
  });

  it("drops the snapshot and in-flight answers when the tenant or the member changes", async () => {
    for (const change of [() => tenantStore.setState({ tenant: "b" }), () => tenantStore.setState((x) => ({ identityRev: x.identityRev + 1 }))]) {
      const reqs = deferred();
      await mount();
      act(() => latest!.reload());
      await act(async () => reqs[0]({ email: "a@example.com" }));
      expect(latest!.conns).toEqual({ email: "a@example.com" });
      act(() => latest!.reload());
      act(() => change());
      await act(async () => reqs[1]({ email: "late@example.com" }));
      expect(latest!.conns).toBeNull();
      roots.splice(0).forEach((r) => act(() => r.unmount()));
    }
  });
});
