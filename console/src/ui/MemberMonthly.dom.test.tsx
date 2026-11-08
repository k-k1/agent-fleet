import { describe, expect, it } from "vitest";
import { act, type ReactElement } from "react";
import { createRoot } from "react-dom/client";
import { MemberMonthlyTable, costMemberRows, usageMemberRows } from "./MemberMonthly.tsx";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

async function mount(el: ReactElement) {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => root.render(el));
  return { host, done: async () => { await act(async () => root.unmount()); host.remove(); } };
}

const names = (host: HTMLElement) => [...host.querySelectorAll("td.mm-name .mono")].map((e) => e.textContent);

describe("MemberMonthlyTable", () => {
  const members = usageMemberRows([
    { membership_id: "a", user_key: "aoi", tenant: "sales", total_secs: 3600, months: [
      { month: "2026-07", running_secs: 3600, partial: false },
      { month: "2026-08", running_secs: 0, partial: true, change_pct: -100 } ] },
    { membership_id: "b", user_key: "ben", tenant: "sales", total_secs: 7200, months: [
      { month: "2026-07", running_secs: 3600, partial: false },
      { month: "2026-08", running_secs: 3600, partial: true, change_pct: 0 } ] },
    { membership_id: "c", user_key: "", tenant: "sales", total_secs: 100, months: [
      { month: "2026-07", running_secs: 0, partial: false },
      { month: "2026-08", running_secs: 100, partial: true } ] },
  ]);
  const fmt = (v: number) => `${v / 3600}h`;

  it("sorts by the chosen key, marks the running month and prints the change", async () => {
    const { host, done } = await mount(
      <MemberMonthlyTable title="T" members={members} format={fmt} unknownLabel="?" totalMembers={9} />,
    );
    expect(names(host)).toEqual(["ben", "aoi", "?"]);
    expect(host.querySelector('td[data-month="2026-08"]')!.className).toContain("partial");
    expect(host.textContent).toContain("-100.0%");
    expect(host.textContent).toContain("0.0%");
    expect(host.querySelector(".mt-note")!.textContent).toContain("3");
    const sel = host.querySelector("select")!;
    await act(async () => {
      sel.value = "name";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(names(host)).toEqual(["?", "aoi", "ben"]);
    await act(async () => {
      sel.value = "change";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(names(host)[0]).toBe("ben");
    await done();
  });

  it("shows a month before coverage as no data, never a zero", async () => {
    const rows = costMemberRows([
      { membership_id: "a", user_key: "aoi", total_unblended_micro: 5, months: [
        { month: "2026-06", unblended_micro: 0, no_coverage: true },
        { month: "2026-07", unblended_micro: 5_000_000, partial: true } ] },
    ]);
    const { host, done } = await mount(
      <MemberMonthlyTable title="T" members={rows} format={(v) => `$${v / 1e6}`} unknownLabel="?" />,
    );
    const june = host.querySelector('td[data-month="2026-06"]')!;
    expect(june.textContent).not.toContain("$0");
    expect(june.textContent).toBeTruthy();
    expect(host.querySelector(".mt-note")).toBeNull();
    await done();
  });
});
