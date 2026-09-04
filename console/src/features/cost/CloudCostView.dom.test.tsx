// The cloud cost screens (docs/log/67, ADR 0048). What these hold down is not the numbers but the
// three ways their meaning breaks:
//   1. the personal heading must not read "your cost" - measured, only about 20% of the bill
//      attaches to a person, so shortening it calls a fifth of what the company pays "your cost"
//      (ADR 0048 decision 4);
//   2. shared infrastructure amounts, other people's amounts and the deployment total must never
//      reach a personal screen, or someone else's share follows by subtraction;
//   3. a period before tag activation is reported as unavailable, not as zero - activation does not
//      backfill, so that 0 stays 0 for ever and never corrects itself.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { MyCloudCostView, CloudCostAdminView, MemberCostPanel } from "./CloudCostView.tsx";

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
});

const text = () => host?.textContent || "";

const meta = (extra: Record<string, unknown> = {}) => ({
  currency: "USD",
  first_day: "2026-08-17",
  last_day: "2026-08-20",
  estimated: true,
  lag_hours: 24,
  profile: { runtime: "ecs-ec2", available: true, verified: true, shared: ["nat", "dns"] },
  ...extra,
});

describe("MyCloudCostView", () => {
  it("builds the amount from micro units and keeps the currency AWS returned", async () => {
    api.mockResolvedValue({
      from: "2026-08-17",
      to: "2026-08-20",
      total_micro: 2_345_678,
      days: [{ day: "2026-08-17", unblended_micro: 2_345_678, estimated: false }],
      services: [{ service: "Amazon EC2", unblended_micro: 2_345_678 }],
      meta: meta(),
    });
    await mount(<MyCloudCostView />);
    // 2345678 micro = $2.35. Never converted to yen: converted, it stops being an invoice.
    expect(text()).toMatch(/\$2\.35/);
    expect(text()).not.toMatch(/￥|¥/);
  });

  it("the heading says costs attached directly to you, shared costs excluded, never 'your cost'", async () => {
    api.mockResolvedValue({ total_micro: 1_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("直接ひも付く費用");
    expect(text()).toContain("共有分は含みません");
  });

  it("shows nothing that would let someone else's share be worked out", async () => {
    api.mockResolvedValue({ total_micro: 1_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    // The shared-infrastructure card does not exist on the personal screen, and the server does
    // not return it either.
    expect(host?.querySelector(".cloud-cost .admin-panel h4")?.textContent).not.toContain("共有インフラ");
    expect(text()).not.toContain("共有（割り当てなし）");
    expect(text()).not.toContain("メンバーにひも付く費用");
  });

  it("asked about a period before tag activation, it says the data cannot be fetched, not zero", async () => {
    api.mockResolvedValue({ total_micro: 0, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    // The default range (the last 30 days) starts before first_day.
    const from = host?.querySelectorAll<HTMLInputElement>('input[type="date"]')[0];
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
      setter.call(from, "2026-08-01");
      from!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("遡って埋めることはできません");
  });

  it("puts the lag and the not-final note beside the numbers, not in a footnote", async () => {
    api.mockResolvedValue({ total_micro: 5_000_000, days: [], services: [], meta: meta() });
    await mount(<MyCloudCostView />);
    const notes = host?.querySelector(".cc-notes");
    expect(notes?.textContent).toContain("24 時間遅れ");
    expect(notes?.textContent).toContain("まだ確定しておらず");
    // It has to sit right next to the headline amount, inside the same panel.
    expect(host?.querySelector(".admin-panel .cc-headline + .cc-notes")).toBeTruthy();
  });

  it("shows the reason, not a blank, when Cost Explorer cannot be read", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ error: "AccessDeniedException: not authorized" }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("Cost Explorer を読めていない");
    expect(text()).toContain("AccessDeniedException");
  });
});

describe("CloudCostAdminView", () => {
  const tenants = [{ slug: "acme", name: "Acme" }];

  it("shows shared infrastructure with its breakdown to a super_admin", async () => {
    api.mockResolvedValue({
      members: [{ membership_id: "M-1", user_key: "alice", email: "a@x.com", unblended_micro: 2_000_000 }],
      attributed_micro: 2_000_000,
      shared_micro: 9_000_000,
      shared_services: [{ service: "Amazon Route 53", unblended_micro: 9_000_000 }],
      meta: meta(),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={true} />);
    expect(text()).toContain("共有インフラ");
    expect(text()).toMatch(/\$9\.00/);
    expect(text()).toContain("Amazon Route 53");
    // The sentence that tells the reader it is not divided per head.
    expect(text()).toContain("割り振っていません");
  });

  it("draws no shared card when the tenant admin's response carries no shared costs", async () => {
    // The screen never decides who sees what: it just does not draw what the server did not return.
    api.mockResolvedValue({
      members: [{ membership_id: "M-1", user_key: "alice", email: "a@x.com", unblended_micro: 2_000_000 }],
      attributed_micro: 2_000_000,
      meta: meta(),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={false} />);
    expect(text()).toContain("メンバーにひも付く費用");
    // The intro does explain that shared costs are handled separately - the reader needs to know a
    // member total is not the bill - so what is checked for absence is the card itself.
    const headings = Array.from(host!.querySelectorAll("h4")).map((h) => h.textContent);
    expect(headings).not.toContain("共有インフラ");
    expect(text()).not.toContain("共有（割り当てなし）");
  });

  it("says the numbers may be incomplete on an unverified runtime", async () => {
    api.mockResolvedValue({
      members: [],
      attributed_micro: 0,
      meta: meta({ profile: { runtime: "ecs", available: true, verified: false } }),
    });
    await mount(<CloudCostAdminView tenants={tenants} isSuper={true} />);
    expect(text()).toContain("実環境でまだ確認できていない");
  });
});

describe("cost allocation tag state", () => {
  // An axis that is not active yet is not "still loading": the cost meanwhile is lost permanently,
  // so this has to stay prominently visible for the whole wait.
  it("warns that cost is being lost while an axis is still unregistered", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership"], pending: ["af-tenant"] } }),
    });
    await mount(<MyCloudCostView />);
    const warn = host?.querySelector(".cc-notes .form-err");
    expect(warn?.textContent).toContain("af-tenant");
    expect(warn?.textContent).toContain("あとから取り戻せません");
  });

  it("shows the reason when automatic activation failed", async () => {
    api.mockResolvedValue({
      total_micro: 0,
      days: [],
      services: [],
      meta: meta({ tags: { pending: ["af-tenant"], error: "AccessDeniedException: ce:UpdateCostAllocationTagsStatus" } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("自動で有効化できませんでした");
    expect(text()).toContain("AccessDeniedException");
  });

  // What a person switched off in the billing console is not switched back on by the CP, and the
  // screen states it as a fact, not a warning - a warning would read as "fix this".
  it("an axis a person disabled is shown as a note, not a warning", async () => {
    api.mockResolvedValue({
      total_micro: 1_000_000,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership"], declined: ["af-slot-size"] } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).toContain("そのままにしています");
    expect(host?.querySelector(".cc-notes .form-err")).toBeNull();
  });

  // On an organisation member account the activation state is permanently unreadable, so the CP
  // keeps the reason in `error` while reporting through `attributed` that attribution is confirmed
  // in practice. Miss that and "attributed to nobody" stays printed directly above amounts that
  // are correctly attributed.
  it("no unreadable-state warning once attribution is confirmed by the actual numbers", async () => {
    api.mockResolvedValue({
      total_micro: 730_000,
      days: [],
      services: [],
      meta: meta({
        tags: {
          attributed: ["af-membership"],
          error: "activation state is not readable from a member account (only the payer may activate)",
        },
      }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).not.toContain("自動で有効化できませんでした");
    expect(text()).not.toContain("取り戻せません");
    // The CP's raw English explanation is not put on screen either.
    expect(text()).not.toContain("payer");
    expect(host?.querySelector(".cc-notes .form-err")).toBeNull();
  });

  it("adds nothing when every axis is active", async () => {
    api.mockResolvedValue({
      total_micro: 1_000_000,
      days: [],
      services: [],
      meta: meta({ tags: { active: ["af-membership", "af-tenant"] } }),
    });
    await mount(<MyCloudCostView />);
    expect(text()).not.toContain("取り戻せません");
    expect(text()).not.toContain("そのままにしています");
  });
});

// Cloud cost on member detail (docs/log/67 §67.15). The same three points as the personal view,
// plus a fourth: on a deployment with no bill the screen must not exist at all, because a screen of
// zeros reads as "free".
describe("MemberCostPanel", () => {
  // This component calls profile and then cost, so the mock routes on the URL.
  const route = (cost: Record<string, unknown>, available = true) => {
    api.mockImplementation((url: string) => {
      if (url.startsWith("api/cost/profile")) {
        return Promise.resolve({ runtime: "ecs-ec2", available, verified: true });
      }
      return Promise.resolve(cost);
    });
  };

  const oneMember = {
    total_micro: 2_500_000,
    days: [
      { day: "2026-08-17", unblended_micro: 2_000_000, estimated: false },
      { day: "2026-08-18", unblended_micro: 500_000, estimated: true },
    ],
    services: [
      { service: "Amazon EC2", unblended_micro: 2_000_000 },
      { service: "Amazon Elastic Block Store", unblended_micro: 500_000 },
    ],
    meta: meta(),
  };

  it("never says this member's cost and states it is what attaches directly", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toContain("直接ひも付く費用");
    expect(text()).toContain("共有分は含みません");
    // Shortened, this would call a fifth of what the company pays "this member's cost".
    expect(text()).not.toContain("このメンバーのコスト");
  });

  it("shows the daily bars and breakdown the list lacks, not just a repeat of the total", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toMatch(/\$2\.50/);
    expect(host?.querySelectorAll(".cc-day").length).toBe(2);
    // An unsettled day must not read with the same weight as a settled one.
    expect(host?.querySelectorAll(".cc-day.est").length).toBe(1);
    expect(host?.querySelectorAll(".usage-row.cc-svc").length).toBe(2);
    expect(text()).toContain("Amazon Elastic Block Store");
  });

  it("fetches only that member, from the per-member endpoint", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w@acme.co.jp" />);
    const urls = api.mock.calls.map((c) => String(c[0]));
    // user_key can be an email address, so the URL is always built with escaping.
    expect(urls).toContain("api/admin/tenants/sales/members/w%40acme.co.jp/cost");
    // The list endpoint (every member of the tenant) is not called: one person is not fetched by
    // fetching everyone.
    expect(urls.some((u) => u.startsWith("api/admin/cloud-cost"))).toBe(false);
  });

  it("shows neither shared infrastructure, nor other people's amounts, nor the deployment total", async () => {
    route(oneMember);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    // The intro does say shared infrastructure is not included, which is its whole point. What
    // must not appear is a shared amount, so the check looks at the card heading and total label.
    const heads = [...(host?.querySelectorAll("h4") || [])].map((h) => h.textContent || "");
    expect(heads.some((h) => h.includes("共有インフラ"))).toBe(false);
    expect(text()).not.toContain("共有（割り当てなし）");
    expect(text()).not.toContain("メンバーにひも付く費用");
  });

  it("keeps the caveats right beside the total, so a 0 is not read as free", async () => {
    route({ total_micro: 0, days: [], services: [], meta: meta() });
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(host?.querySelector(".cc-headline + .cc-notes")).toBeTruthy();
    expect(host?.querySelector(".cc-notes")?.textContent).toContain("24 時間遅れ");
  });

  it("draws nothing and calls no admin API on a deployment with no bill", async () => {
    route(oneMember, false);
    await mount(<MemberCostPanel slug="sales" userKey="w-acme-co-jp" />);
    expect(text()).toBe("");
    expect(api.mock.calls.map((c) => String(c[0])).every((u) => u.startsWith("api/cost/profile"))).toBe(true);
  });
});
