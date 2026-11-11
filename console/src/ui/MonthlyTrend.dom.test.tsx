import { describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MonthlyTrend, costMonthRows, usageMonthRows } from "./MonthlyTrend.tsx";

describe("MonthlyTrend", () => {
  it("shows no-data months as unknown, marks the running month and prints the change", async () => {
    const rows = costMonthRows([
      { month: "2026-06", unblended_micro: 0, no_coverage: true, partial: false },
      { month: "2026-07", unblended_micro: 1_000_000, partial: true },
      { month: "2026-08", unblended_micro: 1_500_000, partial: true, estimated: true, change_pct: 50 },
    ]);
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<MonthlyTrend title="T" rows={rows} format={(v) => `$${v / 1e6}`} />);
    });
    const june = host.querySelector('[data-month="2026-06"]')!;
    expect(june.className).toContain("nodata");
    expect(june.textContent).not.toContain("$0");
    expect(host.textContent).toContain("+50.0%");
    expect(host.querySelector('[data-month="2026-08"]')!.className).toContain("partial");
    await act(async () => root.unmount());
    host.remove();
  });

  it("words a coverage-truncated past month as partial data, not as still running", async () => {
    const rows = costMonthRows([
      { month: "2026-08", unblended_micro: 0, partial: true, coverage_from: "2026-08-20" },
      { month: "2026-09", unblended_micro: 2_000_000, partial: true },
    ]);
    expect(rows[0].coverageFrom).toBe("2026-08-20");
    expect(rows[1].coverageFrom).toBeUndefined();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<MonthlyTrend title="T" rows={rows} format={(v) => `$${v / 1e6}`} />);
    });
    const aug = host.querySelector('[data-month="2026-08"]')!;
    expect(aug.textContent).toMatch(/(partial data \(from 2026-08-20\)|一部のデータ（2026-08-20から）)/);
    expect(aug.textContent).not.toMatch(/\(\(|（（|\(一部|\(partial/);
    expect(aug.textContent).not.toMatch(/so far|途中/);
    expect(aug.className).toContain("partial");
    // An older CP sends only partial: true; that still reads as the running month.
    expect(host.querySelector('[data-month="2026-09"]')!.textContent).toMatch(/so far|途中/);
    await act(async () => root.unmount());
    host.remove();
  });

  it("maps usage rows without a change for a zero previous month", () => {
    const rows = usageMonthRows([{ month: "2026-08", running_secs: 3600, partial: true }]);
    expect(rows[0].changePct).toBeNull();
  });
});
