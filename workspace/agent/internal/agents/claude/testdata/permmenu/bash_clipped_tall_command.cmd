cd /home/dev/repos/agent-fleet@feature-3383-monthly-usage-cost-view/console; cat > src/ui/MonthlyTrend.dom.test.tsx <<'EOF'
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

  it("maps usage rows without a change for a zero previous month", () => {
    const rows = usageMonthRows([{ month: "2026-08", running_secs: 3600, partial: true }]);
    expect(rows[0].changePct).toBeNull();
  });
});
EOF
npx tsc --noEmit -p . 2>&1 | tail