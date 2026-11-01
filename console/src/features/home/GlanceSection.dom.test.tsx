// "At a glance" on Home (#2362): the stopped, empty and populated states, and the cost rules —
// folded costs nothing, Machine registers as a stats-feed viewer only while expanded, limits are
// read from the chips' store with no request of their own, and the model tile makes one
// unforced /usage/series request.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn((_p: string, ..._r: unknown[]): Promise<unknown> => Promise.resolve({}));
vi.mock("../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (p: string, ...r: unknown[]) => api(p, ...r),
}));
vi.mock("../usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));
vi.mock("../../app/usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));

let mobile = false;
vi.mock("../../lib/device.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useIsMobile: () => mobile,
}));

let viewerMounts = 0;
let viewerUnmounts = 0;
let samples: unknown[] = [];
vi.mock("../../core/store/wsStatsFeed.ts", async (importOriginal) => {
  const React = await import("react");
  return {
    ...(await importOriginal<Record<string, unknown>>()),
    useWsStatsSeries: () => {
      React.useEffect(() => {
        viewerMounts++;
        return () => void viewerUnmounts++;
      }, []);
      return samples;
    },
  };
});

const openSettings = vi.fn();
const { useSettingsUI } = await import("../settings/store.ts");
const { GlanceSection } = await import("./GlanceSection.tsx");
const { HomeView } = await import("./HomeView.tsx");
const { UsageChip } = await import("../../app/wsbar/UsageChip.tsx");
const { USAGE_SOURCES } = await import("../../app/wsbar/usageSources.ts");
const { USAGE_ALL } = await import("../../app/wsbar/usageAll.ts");
const { recordUsageReading, useUsageReadings } = await import("../../app/wsbar/usageReadings.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { PaneHoverProvider } = await import("../../lib/panehover.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement;

const HOUR = 3600000;
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};
const calls = () => api.mock.calls.map((c) => String(c[0]));
const tile = (id: string) => host.querySelector(`[data-tile="${id}"]`);
const LIMIT_PATHS = /api\/(claude|codex|muse|copilot)\/usage|api\/connections\/agy\/usage|api\/usage\/all/;

const hourKey = (ms: number) => new Date(ms).toISOString().slice(0, 13);
const uptimeBody = () => ({
  interval_secs: 60,
  observed: [{ hour: hourKey(Date.now()), samples: 60 }],
  members: [{ tenant: "", user_key: "u", email: "", hours: [{ hour: hourKey(Date.now()), samples: 60, running_secs: 3000, measured_secs: 3000, session_secs: 0 }] }],
});
const seriesBody = (spend: number, calls: number, unmeasured = 0) => ({
  from: "", to: "", bucket: "hour", by: "model",
  buckets: spend ? [{ t: "x", series: { "model-a": { spend, in: 0, out: 0, cread: 0, ccreate: 0, calls, cost_est_usd: 1.5 } } }] : [],
  totals: { spend, in: 0, out: 0, cread: 0, ccreate: 0, calls, cost_est_usd: spend ? 1.5 : 0 },
  coverage: {}, unmeasured_calls: unmeasured,
});

let uptime: unknown = {};
let series: unknown = seriesBody(0, 0);
const route = (p: string) => {
  if (p.startsWith("api/usage/me/hourly")) return Promise.resolve(uptime);
  if (p.startsWith("api/usage/series")) return Promise.resolve(series);
  return Promise.resolve({});
};

const mount = async (node: React.ReactNode) => {
  await act(async () => root!.render(node));
  await settle();
};

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  api.mockReset();
  api.mockImplementation(route);
  viewerMounts = viewerUnmounts = 0;
  samples = [];
  mobile = false;
  uptime = {};
  series = seriesBody(0, 0);
  openSettings.mockReset();
  useSettingsUI.setState({ openSettings } as never);
  useWorkspaceStore.setState({ state: "running" });
  useTenantStore.setState({ tenant: "t" } as never);
  useSessionsStore.setState({ sessions: [] });
  useUsageReadings.setState({ tenant: "", byKind: {} });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("At a glance: states", () => {
  it("empty (fresh workspace): no tile claims a figure, and the section says so", async () => {
    await mount(<GlanceSection />);
    expect(host.querySelector(".home-glance")).not.toBeNull();
    expect(host.querySelector('[data-tile="uptime"]')).toBeNull();
    expect(tile("models")).toBeNull();
    expect(tile("limits")).toBeNull();
    expect(tile("agents")).toBeNull();
    // Machine is up but nothing has been measured yet: "no reading", never 0.
    expect(tile("machine")!.textContent).toContain(t("home.glance_nodata"));
    expect(tile("machine")!.textContent).not.toMatch(/\b0%/);
  });

  it("stopped: machine and model tiles say stopped, uptime and limits still render, no agent-side request", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    uptime = uptimeBody();
    recordUsageReading("t", "claude", { ok: true, ageSec: 10, fiveHour: { pct: 30, resetsAt: new Date(Date.now() + HOUR).toISOString() } });
    await mount(<GlanceSection />);
    expect(tile("machine")!.textContent).toContain(t("home.glance_stopped"));
    expect(tile("models")!.textContent).toContain(t("home.glance_stopped"));
    expect(tile("uptime")).not.toBeNull();
    expect(tile("limits")!.textContent).toContain("30%");
    expect(viewerMounts).toBe(0);
    expect(calls().some((c) => c.startsWith("api/usage/series"))).toBe(false);
  });

  it("populated: every tile shows the numbers of its source, with ≈ on cost", async () => {
    uptime = uptimeBody();
    series = seriesBody(250000, 7);
    samples = [{ t: 1, cpu: 40, memUsed: 3 * 1073741824, memMax: 8 * 1073741824, diskUsed: 10 * 1073741824, diskTotal: 50 * 1073741824, oom: false, memPressure: null }];
    useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", alive: true, state: "working" }, { name: "s2", kind: "claude", alive: true, state: "question" }] as never });
    recordUsageReading("t", "claude", { ok: true, ageSec: 5, fiveHour: { pct: 62, resetsAt: new Date(Date.now() + HOUR).toISOString() }, sevenDay: { pct: 81, resetsAt: new Date(Date.now() + 30 * HOUR).toISOString() } });
    await mount(<GlanceSection />);
    expect(tile("machine")!.textContent).toContain("40%");
    expect(tile("machine")!.textContent).toContain("3.00 GiB / 8.00 GiB");
    expect(tile("uptime")!.textContent).toContain(t("home.glance_idle", { time: t("home.glance_dur_m", { m: 50 }) }));
    expect(tile("agents")!.textContent).toBe(t("home.glance_agents") + t("home.glance_agents_line", { running: 1, busy: 1, waiting: 1 }));
    expect(tile("models")!.textContent).toContain("model-a");
    expect(tile("models")!.textContent).toContain("≈");
    expect(tile("limits")!.textContent).toContain("62%");
    expect(tile("limits")!.textContent).toContain("81%");
    expect(host.querySelectorAll(".glance-strip .uh-cell")).toHaveLength(7);
  });

  it("calls that report no tokens read '—' with the note, never 0", async () => {
    series = seriesBody(0, 4, 4);
    await mount(<GlanceSection />);
    const text = tile("models")!.textContent!;
    expect(text).toContain("—");
    expect(text).toContain(t("usage.coverage_unmeasured", { n: 4 }));
    expect(text).not.toContain("≈");
  });

  it("a stale limit shows its note and '—'; a passed reset reads 'reset not yet observed'", async () => {
    recordUsageReading("t", "codex", {
      ok: true, ageSec: 7200,
      fiveHour: { pct: 0, stale: true, resetsAt: new Date(Date.now() + HOUR).toISOString() },
      sevenDay: { pct: 90, resetsAt: new Date(Date.now() - HOUR).toISOString() },
    });
    await mount(<GlanceSection />);
    const text = tile("limits")!.textContent!;
    expect(text).toContain(t("wsbar.usage.stale_note"));
    expect(text).toContain(t("limits.reset_not_observed"));
    expect(text).not.toMatch(/\b0%|90%/);
  });

  it("each tile opens its detailed view", async () => {
    uptime = uptimeBody();
    series = seriesBody(10, 1);
    useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", alive: true }] as never });
    await mount(<GlanceSection />);
    const click = (id: string) => act(async () => (tile(id)!.querySelector("button.glance-link") as HTMLButtonElement).click());
    await click("machine");
    await click("uptime");
    await click("models");
    expect(openSettings.mock.calls.map((c) => c[0])).toEqual(["machine", "uptime", "usage"]);
  });
});

describe("At a glance: cost", () => {
  it("folded: no stats-feed viewer and no request; the state survives a reload", async () => {
    await mount(<GlanceSection />);
    expect(viewerMounts).toBe(1);
    const toggle = host.querySelector(".home-glance-toggle") as HTMLButtonElement;
    await act(async () => toggle.click());
    expect(viewerUnmounts).toBe(1);
    expect(localStorage.getItem("af-home-glance")).toBe("0");
    api.mockClear();
    await act(async () => root!.unmount());
    root = createRoot(host);
    viewerMounts = 0;
    await mount(<GlanceSection />);
    expect(host.querySelector(".glance-grid")).toBeNull();
    expect(viewerMounts).toBe(0);
    expect(api).not.toHaveBeenCalled();
  });

  it("is folded by default on a phone unless the member opened it", async () => {
    mobile = true;
    await mount(<GlanceSection />);
    expect(host.querySelector(".glance-grid")).toBeNull();
    expect(api).not.toHaveBeenCalled();
    await act(async () => (host.querySelector(".home-glance-toggle") as HTMLButtonElement).click());
    expect(localStorage.getItem("af-home-glance")).toBe("1");
    expect(host.querySelector(".glance-grid")).not.toBeNull();
  });

  it("registers no stats-feed viewer while the page is hidden", async () => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    try {
      await mount(<GlanceSection />);
      expect(viewerMounts).toBe(0);
    } finally {
      delete (document as unknown as { hidden?: boolean }).hidden;
    }
  });

  it("makes exactly one unforced 24 h hourly by-model series request", async () => {
    series = seriesBody(10, 1);
    await mount(<GlanceSection />);
    const s = calls().filter((c) => c.startsWith("api/usage/series"));
    expect(s).toHaveLength(1);
    const q = new URLSearchParams(s[0].split("?")[1]);
    expect(q.get("bucket")).toBe("hour");
    expect(q.get("by")).toBe("model");
    expect(q.has("fold")).toBe(false);
    expect(Date.parse(q.get("to")!) - Date.parse(q.get("from")!)).toBe(24 * HOUR);
    expect(calls().filter((c) => c.startsWith("api/usage/me/hourly"))).toHaveLength(1);
  });

  it("mounting Home adds no request to any usage-limit endpoint: the chips' reading is read from the store", async () => {
    api.mockImplementation((p: string) =>
      p.startsWith(USAGE_ALL)
        ? Promise.resolve({ kinds: { claude: { ok: true, authed: true, ageSec: 3, fiveHour: { pct: 44, resetsAt: new Date(Date.now() + HOUR).toISOString() } } } })
        : route(p),
    );
    // The chip is the one poller: it reads /usage/all once and writes the store.
    const src = USAGE_SOURCES.find((s) => s.kind === "claude")!;
    await mount(<UsageChip src={src} tenant="t" />);
    expect(calls().filter((c) => c.startsWith(USAGE_ALL))).toHaveLength(1);
    expect(useUsageReadings.getState().byKind.claude.reading.fiveHour?.pct).toBe(44);

    api.mockClear();
    await act(async () => root!.unmount());
    root = createRoot(host);
    await mount(
      <ToastProvider>
        <ConfirmProvider>
          <PaneHoverProvider>
            <HomeView embedded />
          </PaneHoverProvider>
        </ConfirmProvider>
      </ToastProvider>,
    );
    expect(tile("limits")!.textContent).toContain("44%");
    expect(calls().filter((c) => LIMIT_PATHS.test(c))).toEqual([]);
  });

  it("folding and re-expanding repeats no read: one series and one hourly request in total", async () => {
    uptime = uptimeBody();
    series = seriesBody(10, 1);
    await mount(<GlanceSection />);
    const toggle = () => act(async () => (host.querySelector(".home-glance-toggle") as HTMLButtonElement).click());
    await toggle();
    await toggle();
    await toggle();
    await toggle();
    await settle();
    expect(host.querySelector(".glance-grid")).not.toBeNull();
    expect(tile("models")).not.toBeNull();
    expect(calls().filter((c) => c.startsWith("api/usage/series"))).toHaveLength(1);
    expect(calls().filter((c) => c.startsWith("api/usage/me/hourly"))).toHaveLength(1);
  });

  it("starts no read until the section is first expanded", async () => {
    localStorage.setItem("af-home-glance", "0");
    await mount(<GlanceSection />);
    expect(api).not.toHaveBeenCalled();
    await act(async () => (host.querySelector(".home-glance-toggle") as HTMLButtonElement).click());
    await settle();
    expect(calls().filter((c) => c.startsWith("api/usage/series"))).toHaveLength(1);
  });

  it("passes the abort signal to both reads and aborts them when Home goes away", async () => {
    api.mockImplementation(() => new Promise(() => {}));
    await mount(<GlanceSection />);
    const signals = api.mock.calls
      .filter((c) => /usage\/(me\/hourly|series)/.test(String(c[0])))
      .map((c) => (c[1] as RequestInit | undefined)?.signal);
    expect(signals).toHaveLength(2);
    expect(signals.every((x) => x instanceof AbortSignal && !x.aborted)).toBe(true);
    await act(async () => root!.unmount());
    root = createRoot(host);
    expect(signals.every((x) => x!.aborted)).toBe(true);
  });

  it("never shows one workspace's figures as another's while the new one is pending or failed", async () => {
    uptime = uptimeBody();
    series = seriesBody(10, 1);
    await mount(<GlanceSection />);
    expect(tile("uptime")).not.toBeNull();
    expect(tile("models")).not.toBeNull();

    // Tenant b: the reads never answer.
    api.mockImplementation(() => new Promise(() => {}));
    await act(async () => useTenantStore.setState({ tenant: "b" } as never));
    await settle();
    expect(tile("uptime")).toBeNull();
    expect(tile("models")).toBeNull();

    // Tenant c: the reads fail; a's data still must not come back.
    api.mockImplementation(() => Promise.resolve({ error: { status: 500, code: "boom" } }));
    await act(async () => useTenantStore.setState({ tenant: "c" } as never));
    await settle();
    expect(tile("uptime")).toBeNull();
    expect(tile("models")!.textContent).toContain(t("home.glance_load_error"));
    expect(tile("models")!.textContent).not.toContain("model-a");
  });
});
