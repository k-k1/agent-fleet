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
    expect(tds[8]).toBe("0 min");
    expect(tds[9]).toBe("not measured");
  });

  it("an all-null row shows no zero anywhere in the figure columns", async () => {
    api.mockResolvedValue({ days: [day({})] });
    const h = await mount();
    const tds = [...h.querySelectorAll("tbody tr td")].slice(3).map((t) => t.textContent);
    expect(tds).toEqual(Array(7).fill("not measured"));
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
