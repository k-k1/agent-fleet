// On a phone the usage chips mount only while the ⋯ popover is open, yet Home's "Limits" tile
// reads what the chips read (usageReadings.ts). The bar itself must therefore keep the one usage
// poll alive: readings arrive with ⋯ never opened, and no second request is added.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const HOUR = 3600000;
const api = vi.fn((p: string, ..._r: unknown[]): Promise<unknown> =>
  Promise.resolve(
    String(p).startsWith("api/usage/all")
      ? { kinds: { claude: { ok: true, authed: true, ageSec: 4, fiveHour: { pct: 33, resetsAt: new Date(Date.now() + HOUR).toISOString() } } } }
      : {},
  ),
);
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (p: string, ...r: unknown[]) => api(p, ...r),
}));
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { WsBar } from "./WsBar.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { useUsageReadings } from "./wsbar/usageReadings.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;
const origMatchMedia = window.matchMedia;

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockClear();
  useUsageReadings.setState({ tenant: "", byKind: {} });
  window.matchMedia = ((q: string) => ({
    matches: q.includes("max-width: 760px"),
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  useWorkspaceStore.setState({ state: "running" });
  useTenantStore.setState({ tenant: "t" } as never);
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  window.matchMedia = origMatchMedia;
});

describe("usage readings on a phone", () => {
  it("are recorded with ⋯ never opened, from one /usage/all", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <WsBar />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    for (let i = 0; i < 4; i++) await act(async () => void (await Promise.resolve()));
    expect(host.querySelector(".ws-more-pop")).toBeNull();
    expect(host.querySelector(".ws-usage-btn")).toBeNull();
    expect(useUsageReadings.getState().byKind.claude?.reading.fiveHour?.pct).toBe(33);
    expect(api.mock.calls.filter((c) => String(c[0]).startsWith("api/usage/all"))).toHaveLength(1);
  });
});
