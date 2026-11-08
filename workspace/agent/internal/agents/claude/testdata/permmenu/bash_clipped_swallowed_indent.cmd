cat > $AF_WORK_DIR/mk2.py <<'PYEOF'
import sys
dash = "╌"*67
lab = "────────────────── [AF:squkqid] #3383 Monthly usage and cost view ─"
top = r''' │ import { createRoot } from "react-dom/client";
 │ import { MonthlyTrend, costMonthRows, usageMonthRows } from
 │ "./MonthlyTrend.tsx";
 │
 │ describe("MonthlyTrend", () => {
 │   it("shows no-data months as unknown, marks the running month
 │ and prints the change", async () => {
 │     const rows = costMonthRows([
 │       { month: "2026-06", unblended_micro: 0, no_coverage:
 │ true, partial: false },
 │       { month: "2026-07", unblended_micro: 1_000_000, partial:
 │ true },
 │       { month: "2026-08", unblended_micro: 1_500_000, partial:
 │ true, estimated: true, change_pct: 50 },
 │     ]);
 │     const host = document.createElement("div");
 │     document.body.append(host);
 │     const root = createRoot(host);
 │     await act(async () => {
 │       root.render(<MonthlyTrend title="T" rows={rows}
 │ format={(v) => `$${v / 1e6}`} />);
 │     });
 │     const june = host.querySelector('[data-month="2026-06"]')!;
 │     expect(june.className).toContain("nodata");
 │     expect(june.textContent).not.toContain("$0");
 │     expect(host.textContent).toContain("+50.0%");
 │     expect(host.querySelector('[data-month="2026-08"]')!.classN
 │ ame).toContain("partial");
 │     await act(async () => root.unmount());
 │     host.remove();
 │   });
 │
 │   it("maps usage rows without a change for a zero previous
 │ month", () => {
 │     const rows = usageMonthRows([{ month: "2026-08",
 │ running_secs: 3600, partial: true }]);
 │     expect(rows[0].changePct).toBeNull();
 │   });
 │ });
 │ EOF
 │ npx tsc --noEmit -p . 2>&1 | tail
DASH
 │ Dangerous rm operation not ruled out — the shell may read this
 │ command differently than it was parsed
 ⚠ Claude Code will automatically block this request in 0:47, to
 avoid holding up an unattended session


 Do you want to proceed?
 ❯ 1. Yes
   2. No

 Esc to cancel · Tab to amend
LAB'''.replace("DASH", dash).replace("LAB", lab)
lines = top.split("\n")
print(len(lines), file=sys.stderr)
open(sys.argv[1], "w").write(top + "\n")
PYEOF
python3 -I $AF_WORK_DIR/mk2.py $AF_WORK_DIR/probe_out.txt; awk '{ if (length($0) > 67) print NR": "length($0) }' $AF_WORK_DIR/probe_out.txt