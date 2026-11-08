// The member-detail view carries the member's monthly running hours (issue #3609).
import { describe, it, expect, beforeAll, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (p: string) => api(p),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

const { MemberUptimePanel } = await import("./UptimeHeatmap.tsx");

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

describe("MemberUptimePanel monthly hours", () => {
  it("fetches the member's usage-monthly and draws the months with the change", async () => {
    api.mockImplementation(async (p: string) =>
      p.includes("usage-monthly")
        ? { monthly: [
            { month: "2026-07", running_secs: 3600, running_hours: 1 },
            { month: "2026-08", running_secs: 7200, running_hours: 2, partial: true, change_pct: 100 } ] }
        : { from: "2026-08-01", to: "2026-08-01", interval_secs: 300, observed: [], members: [] },
    );
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => root.render(<MemberUptimePanel slug="sales" userKey="aoi" />));
    expect(api.mock.calls.map((c) => c[0])).toContain("api/admin/tenants/sales/members/aoi/usage-monthly?months=6");
    expect(host.querySelector('[data-month="2026-08"]')!.textContent).toContain("+100.0%");
    expect(host.querySelector('[data-month="2026-07"]')!.textContent).toContain("1.0 h");
    await act(async () => root.unmount());
    host.remove();
  });
});
