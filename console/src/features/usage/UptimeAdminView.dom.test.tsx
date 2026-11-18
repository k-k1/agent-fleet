// The tenant admin's uptime view carries the tenant-wide usage history roll-up (issue #4493).
import { describe, it, expect, beforeAll, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: (p: string) => api(p),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

const { UptimeAdminView } = await import("./UptimeHeatmap.tsx");

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

describe("UptimeAdminView usage history roll-up", () => {
  it("requests the roll-up for the selected tenant and lists each member's rows", async () => {
    api.mockImplementation(async (p: string) =>
      p.startsWith("api/admin/usage/history")
        ? { days: [{
            day: "2026-11-17", tenant: "sales", user_key: "aoi", email: "a@x.test", instance_type: "m8g.medium",
            vcpu: null, burst_baseline_pct: null, running_minutes: 60,
            measured_minutes: { cpu: 0, working_set: 0, psi: 0, refault: 0 },
            cpu_p50: null, cpu_p95: null, cpu_box_p50: null, cpu_box_p95: null, working_set_p95: null,
            working_set_max: null, psi_full_avg60_max: null, psi_warn_minutes: null, refault_p95: null,
            above_baseline_minutes: null,
          }] }
        : { from: "2026-11-01", to: "2026-11-01", interval_secs: 300, observed: [], members: [] },
    );
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => root.render(<UptimeAdminView tenant="sales" />));
    await act(async () => {});
    const hist = api.mock.calls.map((c) => c[0]).filter((p: string) => p.startsWith("api/admin/usage/history"));
    expect(hist.length).toBeGreaterThan(0);
    expect(hist[0]).toContain("tenant=sales");
    expect(host.querySelector('[data-testid="usage-history"] tbody td:nth-child(2)')!.textContent).toBe("aoi");
    await act(async () => root.unmount());
    host.remove();
  });
});
