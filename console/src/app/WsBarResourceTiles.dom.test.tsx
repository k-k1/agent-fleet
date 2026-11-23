// The WS bar's resources popover: the workspace's own LD tile (dedicated box only), the disk
// tile on every runtime with levels only on the member's own volume, and the "CP host" label
// on the admin host tiles. jsdom has no layout; this proves the wiring, not pixels.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let wsStats: Record<string, unknown> = {};
const api = vi.fn((path: string) => {
  if (path === "api/workspace/stats") return Promise.resolve(wsStats);
  if (path === "api/admin/host") return Promise.resolve({ load1: 3, ncpu: 4, mem_used: 1, mem_total: 2 });
  return Promise.resolve({});
});
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (path: string) => api(path),
}));
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

import { WsBar } from "./WsBar.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { t } from "../lib/i18n/index.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia;
  useWorkspaceStore.setState({ state: "running" });
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
});

const base = { running: true, mem_used: 100, mem_max: 1000, cpu_pct: 5 };

async function open(stats: Record<string, unknown>, superAdmin = false) {
  wsStats = stats;
  useTenantStore.setState({ tenant: "acme", superAdmin });
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
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => host!.querySelector<HTMLButtonElement>(".ws-res-btn")!.click());
  const tiles = new Map<string, Element>();
  host!
    .querySelectorAll(".ws-res-pop .ws-graph")
    .forEach((e) => tiles.set(e.querySelector(".ws-graph-k")!.textContent!, e));
  return tiles;
}

describe("WsBar resources popover", () => {
  it("dedicated box: a member sees LD and a disk tile", async () => {
    const tiles = await open({ ...base, load1: 1.5, ncpu: 2, disk_used: 95, disk_total: 100, home_own: true });
    expect(tiles.get("ld")?.querySelector(".ws-graph-v")?.textContent).toBe("1.50");
    expect(tiles.get("ld")?.className).toContain("ws-graph--warn"); // 1.5 / 2 cores = 0.75
  });

  it("shared host: no LD tile, and the disk tile carries no level", async () => {
    const tiles = await open({ ...base, disk_used: 99, disk_total: 100 });
    expect(tiles.has("ld")).toBe(false);
    expect(tiles.get("dsk")?.querySelector(".ws-graph-v")?.textContent).toBe("99%");
    expect(tiles.get("dsk")?.className).not.toMatch(/ws-graph--(warn|crit)/);
  });

  it("own volume: disk crosses crit", async () => {
    const tiles = await open({ ...base, disk_used: 95, disk_total: 100, home_own: true });
    expect(tiles.get("dsk")?.className).toContain("ws-graph--crit");
  });

  it("missing disk_total renders no dsk tile", async () => {
    const tiles = await open({ ...base, disk_used: 95 });
    expect(tiles.has("dsk")).toBe(false);
  });

  it("super_admin with workspace load: host tiles are labelled CP host", async () => {
    await open({ ...base, load1: 0.5, ncpu: 2 }, true);
    expect(host!.querySelector(".ws-graph-grp")?.textContent).toBe(t("wsbar.tile.cp_host_label"));
    const titles = [...host!.querySelectorAll(".ws-res-pop .ws-graph")].map((e) => e.getAttribute("title") ?? "");
    expect(titles.filter((x) => x.includes(t("wsbar.tile.cp_host_note"))).length).toBe(2);
  });

  it("super_admin without workspace load: no CP host label", async () => {
    await open({ ...base }, true);
    expect(host!.querySelector(".ws-graph-grp")).toBeNull();
  });
});
