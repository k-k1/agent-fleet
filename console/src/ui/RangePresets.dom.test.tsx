// The preset chooser on the range bars: the clock is injected so the dates are pinned, and the
// views are checked to apply the preset's own window (state has not re-rendered when they fetch).
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => "http://localhost/" + p,
}));

import { RangePresets } from "./RangePresets.tsx";
import { CloudCostAdminView } from "../features/cost/CloudCostView.tsx";
import { UsageView } from "../features/settings/tenant/tenantOps.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  vi.useRealTimers();
});

async function choose(value: string) {
  const sel = host!.querySelector<HTMLSelectElement>(".range-presets select")!;
  await act(async () => {
    sel.value = value;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

describe("RangePresets", () => {
  const now = new Date("2027-01-15T10:00:00Z");

  it("hands the January -> December range to the caller, and says months are UTC", async () => {
    const onPick = vi.fn();
    await mount(<RangePresets from="" to="" zone="utc" onPick={onPick} now={now} />);
    expect(host!.textContent).toContain("UTC");
    await choose("last_month");
    expect(onPick).toHaveBeenCalledWith({ from: "2026-12-01", to: "2026-12-31" });
  });

  it("shows Custom for typed dates and picks nothing when Custom is chosen", async () => {
    const onPick = vi.fn();
    await mount(<RangePresets from="2027-01-03" to="2027-01-04" zone="utc" onPick={onPick} now={now} />);
    expect(host!.querySelector<HTMLSelectElement>(".range-presets select")!.value).toBe("custom");
    await choose("custom");
    expect(onPick).not.toHaveBeenCalled();
  });

  it("shows the preset whose range is current", async () => {
    await mount(<RangePresets from="2027-01-01" to="2027-01-15" zone="utc" onPick={() => {}} now={now} />);
    expect(host!.querySelector<HTMLSelectElement>(".range-presets select")!.value).toBe("this_month");
  });
});

describe("clock", () => {
  it.each([["utc"], ["local"]] as const)("reads the clock when a preset is chosen, not when the bar rendered (%s)", async (zone) => {
    vi.useFakeTimers({ toFake: ["Date"], now: new Date("2026-10-31T12:00:00Z") });
    const onPick = vi.fn();
    await mount(<RangePresets from="" to="" zone={zone} onPick={onPick} />);
    // The screen stays open across the month end; nothing re-renders it.
    vi.setSystemTime(new Date("2026-11-02T12:00:00Z"));
    await choose("this_month");
    expect(onPick).toHaveBeenLastCalledWith({ from: "2026-11-01", to: "2026-11-02" });
    await choose("last_month");
    expect(onPick).toHaveBeenLastCalledWith({ from: "2026-10-01", to: "2026-10-31" });
  });
});

describe("a chosen preset going stale", () => {
  it("falls back to Custom once the date passes, so the same preset can be chosen again", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"], now: new Date("2026-10-15T12:00:00Z") });
    const onPick = vi.fn();
    await mount(<RangePresets from="2026-10-01" to="2026-10-15" zone="utc" onPick={onPick} />);
    const sel = () => host!.querySelector<HTMLSelectElement>(".range-presets select")!;
    expect(sel().value).toBe("this_month");
    vi.setSystemTime(new Date("2026-10-16T00:30:00Z"));
    await act(async () => {
      vi.advanceTimersByTime(31_000);
    });
    expect(sel().value).toBe("custom");
    await choose("this_month");
    expect(onPick).toHaveBeenLastCalledWith({ from: "2026-10-01", to: "2026-10-16" });
  });
});

describe("views apply the preset's window", () => {
  it("Cloud cost fetches the chosen month at once", async () => {
    vi.useFakeTimers({ toFake: ["Date"], now: new Date("2026-11-06T12:00:00Z") });
    api.mockResolvedValue({ members: [], attributed_micro: 0, meta: { currency: "USD" } });
    await mount(<CloudCostAdminView tenants={[{ slug: "acme", name: "Acme" }]} isSuper={false} />);
    api.mockClear();
    await choose("last_month");
    expect(api).toHaveBeenCalledTimes(1);
    const url = String(api.mock.calls[0][0]);
    expect(url).toContain("from=2026-10-01");
    expect(url).toContain("to=2026-10-31");
    expect(host!.querySelector<HTMLInputElement>('input[type="date"]')!.value).toBe("2026-10-01");
  });

  it("Usage fetches the chosen month at once", async () => {
    vi.useFakeTimers({ toFake: ["Date"], now: new Date("2026-11-06T12:00:00Z") });
    api.mockResolvedValue({ from: "a", to: "b", totals: [] });
    await mount(<UsageView tenants={[{ slug: "acme", name: "Acme" }] as any} isSuper={false} />);
    api.mockClear();
    await choose("this_month");
    expect(api).toHaveBeenCalledTimes(1);
    const url = String(api.mock.calls[0][0]);
    expect(url).toContain("from=2026-11-01");
    expect(url).toContain("to=2026-11-06");
  });
});
