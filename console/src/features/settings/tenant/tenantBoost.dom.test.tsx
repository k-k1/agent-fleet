// The tenant's Boost policy panel: absent where there are no rungs, lists exactly the rungs the
// deployment declares, and saves through the tenant's own PUT.
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
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MemberBoostLine, TenantBoostView, declaredRungs, fmtBoostHours } from "./tenantBoost.tsx";

const SIZING = {
  slot_classes: [
    { id: "standard", label: "S", arch: "x86_64", slots: [{ instance_type: "a", mem_mib: 8192 }, { instance_type: "b", mem_mib: 16384 }] },
    { id: "arm", label: "A", arch: "arm64", slots: [{ instance_type: "c", mem_mib: 8192 }] },
  ],
};
const policy = (over = {}) => ({ tenant: "acme", enabled: true, max_mem_mib: 0, monthly_hours: 20, editable: true, budget_counted: false, ...over });

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

async function mount(view: unknown) {
  api.mockImplementation((p: string) => Promise.resolve(p.endsWith("/boost") ? view : SIZING));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantBoostView slug="acme" />);
  });
  return host;
}
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset().mockResolvedValue(policy({ max_mem_mib: 16384 }));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("declaredRungs", () => {
  it("is the distinct rung memories across classes, ascending", () => {
    expect(declaredRungs({ ...SIZING, runtime: "x", cpu_effective: false, mem_meaning: "slot", disk_meaning: "home" } as never)).toEqual([8192, 16384]);
  });
});

describe("TenantBoostView", () => {
  it("renders nothing where there is nothing to boost between", async () => {
    const h = await mount(policy({ editable: false }));
    expect(h.textContent).toBe("");
  });

  it("lists the declared rungs, says the budget is not enforced, and saves the chosen rung", async () => {
    const h = await mount(policy());
    expect(h.textContent).toContain("枠は保存されますが強制されません");
    const chip = [...h.querySelectorAll("button.chip")].find((b) => b.textContent?.includes("16 GiB")) as HTMLButtonElement;
    expect(chip).toBeTruthy();
    await act(async () => chip.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/boost", "PUT", { enabled: true, max_mem_mib: 16384, monthly_hours: 20 });
  });

  // Selected = highlighted (`.on`) AND exposed (`aria-pressed`): the two are separate expressions, so
  // each is asserted on its own.
  const selected = (h: HTMLElement) => ({
    on: [...h.querySelectorAll("button.chip.chip--on")].map((b) => b.textContent),
    pressed: [...h.querySelectorAll("button.chip")].filter((b) => b.getAttribute("aria-pressed") === "true").map((b) => b.textContent),
  });

  it("shows which size is currently set, in text, highlight and aria-pressed", async () => {
    const h = await mount(policy({ max_mem_mib: 16384 }));
    expect(h.querySelector(".boost-current")?.textContent).toContain("16 GiB");
    const sel = selected(h);
    expect(sel.on).toEqual([expect.stringContaining("16 GiB")]);
    expect(sel.pressed).toEqual(sel.on);
  });

  it("marks the class-top chip when no cap is set", async () => {
    const h = await mount(policy({ max_mem_mib: 0 }));
    expect(h.querySelector(".boost-current")?.textContent).toContain("クラスの最上位");
    const sel = selected(h);
    expect(sel.on).toEqual(["クラスの最上位"]);
    expect(sel.pressed).toEqual(sel.on);
  });

  it("states the saved value even when it is no longer a declared rung", async () => {
    const h = await mount(policy({ max_mem_mib: 32768 }));
    expect(h.querySelector(".boost-current")?.textContent).toContain("32 GiB");
    expect(selected(h)).toEqual({ on: [], pressed: [] });
  });

  it("hides the bounds while boost is off", async () => {
    const h = await mount(policy({ enabled: false }));
    expect(h.querySelectorAll("button.chip").length).toBe(0);
  });
});

const usage = [
  { user_key: "a", email: "a@example.com", used_hours: 6.5, spent: true, over_hours: 1.5 },
  { user_key: "b", email: "b@example.com", used_hours: 5, spent: true, over_hours: 0 },
  { user_key: "c", used_hours: 2, spent: false, over_hours: 0 },
];

describe("TenantBoostView - this month's hours", () => {
  const rows = (h: HTMLElement) => [...h.querySelectorAll(".boost-usage-list li")].map((li) => li.textContent);

  it("lists each member's hours against the budget and who went past it", async () => {
    const h = await mount(policy({ monthly_hours: 5, budget_counted: true, month: "2026-11", members: usage }));
    expect(h.querySelector(".boost-usage h5")?.textContent).toBe("2026-11 のブースト稼働時間（UTC）");
    expect(rows(h)).toEqual(["a@example.com 6.5 時間 / 枠 5 時間 1.5 時間超過", "b@example.com 5 時間 / 枠 5 時間 枠に到達", "c 2 時間 / 枠 5 時間"]);
    const count = [...h.querySelectorAll(".boost-usage p")].find((p) => p.textContent?.includes("枠に達した"));
    expect(count?.textContent).toBe("枠に達した・超えたメンバー: 2 人");
    expect(count?.className).toBe("warn-text");
  });

  it("shows plain hours and no over-budget count with no budget", async () => {
    const plain = usage.map((u) => ({ ...u, spent: false, over_hours: 0 }));
    const h = await mount(policy({ monthly_hours: 0, budget_counted: true, month: "2026-11", members: plain }));
    expect(rows(h)).toEqual(["a@example.com 6.5 時間", "b@example.com 5 時間", "c 2 時間"]);
    expect(h.textContent).not.toContain("枠に達した");
  });

  it("says nobody boosted when the count is read and empty", async () => {
    const h = await mount(policy({ budget_counted: true, month: "2026-11" }));
    expect(h.querySelector(".boost-usage")?.textContent).toContain("今月ブーストしたサイズで稼働したメンバーはいません");
  });

  it("shows no hours at all when they were not read", async () => {
    const h = await mount(policy({ budget_counted: true, members: usage }));
    expect(h.querySelector(".boost-usage")).toBeNull();
  });

  it("still shows this month's hours after boost is switched off, but not an empty list", async () => {
    let h = await mount(policy({ enabled: false, budget_counted: true, month: "2026-11", members: usage }));
    expect(rows(h)).toHaveLength(3);
    act(() => root?.unmount());
    host?.remove();
    h = await mount(policy({ enabled: false, budget_counted: true, month: "2026-11" }));
    expect(h.querySelector(".boost-usage")).toBeNull();
  });
});

describe("fmtBoostHours", () => {
  it("rounds to one decimal and never shows a sliver as 0", () => {
    expect(fmtBoostHours(0)).toBe("0");
    expect(fmtBoostHours(0.01)).toBe("<0.1");
    expect(fmtBoostHours(1.26)).toBe("1.3");
    expect(fmtBoostHours(40)).toBe("40");
  });
});

describe("MemberBoostLine", () => {
  async function mountLine(view: unknown, userKey: string) {
    api.mockImplementation(() => Promise.resolve(view));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<MemberBoostLine slug="acme" userKey={userKey} />);
    });
    return host;
  }

  it("shows the member's hours against the budget, with the overrun", async () => {
    const h = await mountLine(policy({ monthly_hours: 5, budget_counted: true, month: "2026-11", members: usage }), "a");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/boost");
    expect(h.querySelector(".member-boost p")?.textContent).toBe("ブーストでの稼働: 6.5 時間 / 枠 5 時間（2026-11、UTC） 1.5 時間超過");
  });

  it("reads 0 for a member with no boosted hours when the count was read", async () => {
    const h = await mountLine(policy({ monthly_hours: 5, budget_counted: true, month: "2026-11", members: usage }), "zz");
    expect(h.querySelector(".member-boost p")?.textContent).toBe("ブーストでの稼働: 0 時間 / 枠 5 時間（2026-11、UTC）");
  });

  it("says there is no budget when none is set", async () => {
    const h = await mountLine(policy({ monthly_hours: 0, budget_counted: true, month: "2026-11", members: [{ ...usage[2] }] }), "c");
    expect(h.querySelector(".member-boost p")?.textContent).toBe("ブーストでの稼働: 2 時間（2026-11、UTC、月間の枠なし）");
  });

  it.each([
    ["the hours were not read", policy({ budget_counted: true, members: usage })],
    ["the runtime has no sizes", policy({ editable: false, month: "2026-11", members: usage })],
    ["boost is off and the member has no hours", policy({ enabled: false, month: "2026-11", members: usage })],
  ])("is absent when %s", async (_why, view) => {
    const h = await mountLine(view, "zz");
    expect(h.textContent).toBe("");
  });

  it("is absent when the read fails", async () => {
    api.mockImplementation(() => Promise.reject(new Error("down")));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<MemberBoostLine slug="acme" userKey="a" />);
    });
    expect(host.textContent).toBe("");
  });
});
