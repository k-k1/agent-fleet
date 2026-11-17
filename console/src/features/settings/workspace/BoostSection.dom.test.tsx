// The member's Boost (ADR 0139 decision 5). What is pinned:
//   1. the box and its source/expiry come from the CP's sizing answer, not from the stored size
//   2. a restart is offered only when the CP says the next start is bigger than the running box,
//      and it is a button behind a confirmation, never done on its own
//   3. an unavailable Boost says why, and a spent budget disables the button
//   4. pressing sends the chosen hours; ending a live boost is a DELETE
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  getTenant: () => "default",
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { BoostView } from "./BoostSection.tsx";
import type { WsSizingView, SizingBox } from "./boost.ts";

const small: SizingBox = { class: "standard", instance_type: "m7i.large", mem_mib: 8192 };
const big: SizingBox = { class: "standard", instance_type: "m7i.xlarge", mem_mib: 16384 };

const base = (over: Partial<WsSizingView> = {}, boost: Partial<WsSizingView["boost"]> = {}): WsSizingView => ({
  supported: true,
  runtime: "ecs-ec2",
  stored: small,
  next_start: { box: small, source: "stored" },
  restart_offered: false,
  boost: {
    enabled: true,
    active: false,
    available: true,
    target: big,
    default_hours: 4,
    max_hours: 24,
    budget: { monthly_hours: 0, counted: false, used_hours: 0, spent: false },
    ...boost,
  },
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const reload = vi.fn(async () => {});

async function mount(d: WsSizingView) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<BoostView d={d} reload={reload} />);
  });
  return host;
}
const buttons = () => [...host!.querySelectorAll("button")];
const byText = (s: string) => buttons().find((b) => (b.textContent || "").includes(s));

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset().mockResolvedValue({});
  apiJSON.mockReset().mockResolvedValue({});
  reload.mockClear();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("BoostView", () => {
  it("shows the box the CP resolved and offers Boost with the box it would give", async () => {
    const h = await mount(base());
    expect(h.textContent).toContain("m7i.large");
    expect(byText("m7i.xlarge")).toBeTruthy();
    expect(byText("m7i.xlarge")!.disabled).toBe(false);
  });

  it("names the boost as the source of the next-start box, with its expiry", async () => {
    const h = await mount(
      base({ next_start: { box: big, source: "boost", until: "2026-11-17T18:00:00Z" } }, { active: true, target: big }),
    );
    expect(h.textContent).toContain("m7i.xlarge");
    expect(h.textContent).toContain("ブースト");
    expect(byText("ブーストを終了")).toBeTruthy();
  });

  it("offers a restart only when the CP says so, behind a confirmation", async () => {
    const without = await mount(base({ running: big, next_start: { box: big, source: "boost", until: "2026-11-17T18:00:00Z" } }));
    expect(byText("大きいマシンで再起動")).toBeUndefined();
    act(() => root?.unmount());
    without.remove();

    const h = await mount(
      base({ running: small, restart_offered: true, next_start: { box: big, source: "boost", until: "2026-11-17T18:00:00Z" } }),
    );
    const btn = byText("大きいマシンで再起動")!;
    expect(btn).toBeTruthy();
    await act(async () => btn.click());
    // Pressing the offer opens the confirmation and calls nothing.
    expect(h.ownerDocument.body.textContent).toContain("中で動いているセッションは終了します");
    expect(api).not.toHaveBeenCalledWith("api/workspace/stop", expect.anything());
  });

  it("says a running workspace keeps its box when the next start is not bigger", async () => {
    const h = await mount(base({ running: big, next_start: { box: small, source: "stored" } }));
    expect(h.textContent).toContain("稼働中のワークスペースは今のマシンのままです");
    expect(byText("大きいマシンで再起動")).toBeUndefined();
  });

  it("explains why Boost is unavailable and disables it when the budget is spent", async () => {
    const h = await mount(
      base(
        {},
        { available: false, reason: "budget_spent", budget: { monthly_hours: 10, counted: true, used_hours: 10, spent: true } },
      ),
    );
    expect(h.textContent).toContain("今月のブースト時間を使い切りました");
    expect(h.textContent).toContain("10.0 / 10");
    expect(byText("m7i.xlarge")!.disabled).toBe(true);
  });

  it("says the budget is not enforced while hours cannot be counted", async () => {
    const h = await mount(base({}, { budget: { monthly_hours: 10, counted: false, used_hours: 0, spent: false } }));
    expect(h.textContent).toContain("枠は強制されません");
  });

  it("shows nothing to press when the tenant has not enabled boost", async () => {
    const h = await mount(base({}, { enabled: false, available: false, reason: "disabled" }));
    expect(byText("m7i.xlarge")).toBeUndefined();
    expect(h.textContent).not.toContain("このテナントではブーストが有効になっていません");
  });

  it("presses with the chosen hours and ends a live boost with a DELETE", async () => {
    const h = await mount(base());
    const input = h.querySelector("input[type=number]") as HTMLInputElement;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "8");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => byText("m7i.xlarge")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/workspace/boost", "POST", { hours: 8 });
    expect(reload).toHaveBeenCalled();

    act(() => root?.unmount());
    h.remove();
    await mount(base({ next_start: { box: big, source: "boost", until: "2026-11-17T18:00:00Z" } }, { active: true, target: big }));
    await act(async () => byText("ブーストを終了")!.click());
    expect(api).toHaveBeenCalledWith("api/workspace/boost", { method: "DELETE" });
  });
});
