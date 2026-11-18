// The usage history table must never turn an unmeasured figure into a 0 (ADR 0139 decision 3).
import { describe, it, expect, beforeAll, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (p: string) => api(p),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

const { UsageHistoryTable } = await import("./UsageHistoryTable.tsx");
const { setLocale } = await import("../../lib/i18n/index.ts");

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLocale("en");
});

const day = (over: Record<string, unknown>) => ({
  day: "2026-11-17", instance_type: "m8g.medium", vcpu: null, burst_baseline_pct: null,
  running_minutes: 120, measured_minutes: { cpu: 0, working_set: 0, psi: 0, refault: 0 },
  cpu_p50: null, cpu_p95: null, cpu_box_p50: null, cpu_box_p95: null,
  working_set_p95: null, working_set_max: null, psi_full_avg60_max: null,
  psi_warn_minutes: null, refault_p95: null, above_baseline_minutes: null, ...over,
});

let root: Root | null = null;
let host: HTMLElement | null = null;
async function mount(endpoint = "api/usage/me/history") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<UsageHistoryTable endpoint={endpoint} />));
  await act(async () => {});
  return host;
}
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  api.mockReset();
});

describe("UsageHistoryTable", () => {
  it("renders null as 'not measured' and a real 0 as 0, in the same row", async () => {
    api.mockResolvedValue({ days: [day({
      measured_minutes: { cpu: 60, working_set: 60, psi: 60, refault: 0 },
      cpu_p50: 0, cpu_p95: null, working_set_p95: 536870912, working_set_max: null,
      psi_full_avg60_max: 0, psi_warn_minutes: 0, refault_p95: null,
    })] });
    const h = await mount();
    const tds = [...h.querySelectorAll("tbody tr td")].map((t) => t.textContent ?? "");
    // day, box, running, cpu p50, cpu p95, ws p95, ws max, psi peak, psi warn, refault
    expect(tds[3]).toContain("0.0%");
    expect(tds[3]).not.toContain("not measured");
    expect(tds[4]).toBe("not measured");
    expect(tds[5]).toContain("512 MiB");
    expect(tds[6]).toBe("not measured");
    expect(tds[7]).toContain("0.0%");
    expect(tds[8]).toContain("0.0 min");
    expect(tds[8]).toContain("(60 min measured)");
    // The measured-minutes note rides on every figure of a signal, not just the first.
    expect(tds[3]).toContain("(60 min measured)");
    expect(tds[5]).toContain("(60 min measured)");
    expect(tds[7]).toContain("(60 min measured)");
    expect(tds[9]).toBe("not measured");
  });

  it("an all-null row shows no zero anywhere in the figure columns", async () => {
    api.mockResolvedValue({ days: [day({})] });
    const h = await mount();
    const tds = [...h.querySelectorAll("tbody tr td")].slice(3).map((t) => t.textContent);
    expect(tds).toEqual(Array(7).fill("not measured"));
  });

  it("running minutes: null is not measured, never 0 or NaN; sub-10 keeps its decimal", async () => {
    api.mockResolvedValue({ days: [day({ running_minutes: null }), day({ day: "2026-11-16", running_minutes: 0.5 })] });
    const h = await mount();
    const rows = [...h.querySelectorAll("tbody tr")].map((r) => r.children[2].textContent);
    expect(rows).toEqual(["not measured", "0.5 min"]);
  });

  it("passes the grid's range, widened a day each side for UTC days, to the endpoint", async () => {
    api.mockResolvedValue({ days: [] });
    const host2 = document.createElement("div");
    document.body.append(host2);
    root = createRoot(host2);
    host = host2;
    await act(async () => root!.render(<UsageHistoryTable endpoint="api/usage/me/history" from="2026-11-10" to="2026-11-17" />));
    expect(api).toHaveBeenCalledWith("api/usage/me/history?from=2026-11-09&to=2026-11-18");
  });

  it("requests the given endpoint, shows an error and an empty state", async () => {
    api.mockResolvedValue({ days: [] });
    let h = await mount("api/admin/tenants/t/members/k/usage-history");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/t/members/k/usage-history");
    expect(h.textContent).toContain("No resource history");
    await act(async () => root?.unmount());
    h.remove();
    api.mockRejectedValue(new Error("boom"));
    h = await mount();
    expect(h.querySelector(".form-err")).not.toBeNull();
  });

  it("roll-up: URL-encodes the tenant, names each member, keeps null as not measured", async () => {
    api.mockResolvedValue({ days: [day({ user_key: "aoi", email: "a@x.test", cpu_p50: null })] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () =>
      root!.render(<UsageHistoryTable endpoint="api/admin/usage/history" query={{ tenant: "a&b c" }} from="2026-11-10" to="2026-11-17" roll />),
    );
    await act(async () => {});
    expect(api).toHaveBeenCalledWith("api/admin/usage/history?tenant=a%26b+c&from=2026-11-09&to=2026-11-18");
    const tds = [...host.querySelectorAll("tbody tr td")].map((t) => t.textContent);
    expect(tds[1]).toBe("aoi");
    expect(host.querySelectorAll("th").length).toBe(11);
    expect(tds[5]).toBe("not measured");
    expect(host.textContent).toContain("by member, day and box");
  });

  it("roll-up: omits an empty tenant and adds the tenant column when withTenant", async () => {
    api.mockResolvedValue({ days: [day({ tenant: "sales", user_key: "aoi" })] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<UsageHistoryTable endpoint="api/admin/usage/history" query={{ tenant: "" }} roll withTenant />));
    await act(async () => {});
    expect(api).toHaveBeenCalledWith("api/admin/usage/history");
    const tds = [...host.querySelectorAll("tbody tr td")].map((t) => t.textContent);
    expect(tds.slice(1, 3)).toEqual(["sales", "aoi"]);
  });

  it("roll-up: a stale response for the previous tenant is dropped", async () => {
    let resolveA: (v: unknown) => void = () => {};
    api.mockImplementationOnce(() => new Promise((r) => (resolveA = r)));
    api.mockResolvedValueOnce({ days: [day({ user_key: "bob" })] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const el = (t: string) => <UsageHistoryTable endpoint="api/admin/usage/history" query={{ tenant: t }} roll />;
    await act(async () => root!.render(el("a")));
    await act(async () => root!.render(el("b")));
    await act(async () => {});
    await act(async () => resolveA({ days: [day({ user_key: "alice" })] }));
    expect(host.textContent).toContain("bob");
    expect(host.textContent).not.toContain("alice");
  });

  it("says 未計測 in Japanese, not 0", async () => {
    setLocale("ja");
    api.mockResolvedValue({ days: [day({ cpu_p50: 0 })] });
    const h = await mount();
    const tds = [...h.querySelectorAll("tbody tr td")].map((t) => t.textContent);
    expect(tds[3]).toContain("0.0%");
    expect(tds[4]).toBe("未計測");
    setLocale("en");
  });
});
