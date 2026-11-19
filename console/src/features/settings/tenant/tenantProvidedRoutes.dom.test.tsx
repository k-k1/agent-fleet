// Provided routes on Tenant settings > Model access (ADR 0129 D3; ADR 0138 M2 P10). Pinned here:
//   1. A CP without provided routes (its plain 404) draws nothing; any other failed or partial read
//      draws a "could not be read" line and no editor.
//   2. The policy and member saves PUT exactly the CP's input shapes, built from the loaded values
//      with the edited ones replaced; an emptied member budget goes back to the default (null).
//   3. A refusal stays beside its button with the edits; a value someone else changed since the
//      load is not saved over; the CP's warnings are shown after a save.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { TenantProvidedRoutesView } from "./tenantProvidedRoutes.tsx";
import { DirtyScope } from "../parts/dirtyScope.tsx";
import { TabbedViews } from "../parts/tabbedViews.tsx";
import { noteProvidedAdminRev, noteProvidedRev, resetProvidedRev } from "./providedRevision.ts";
import { fmtDateTime } from "../../../lib/intl.ts";

const PATH = "api/admin/tenants/acme/provided-routes/deployment";
const VIEW = () => ({
  granted: true,
  ceiling_micro: 100_000_000,
  members_only: false,
  member_default_micro: 5_000_000,
  month: "2026-11",
  spent_micro: 1_250_000,
  reserved_micro: 0,
  members: [
    {
      membership_id: "M-a",
      user_key: "alice",
      email: "a@example.com",
      granted: true,
      budget_micro: 20_000_000,
      effective_micro: 20_000_000,
      spent_micro: 1_000_000,
      reserved_micro: 0,
    },
    {
      membership_id: "M-b",
      user_key: "bob",
      email: "b@example.com",
      granted: false,
      budget_micro: null,
      effective_micro: 5_000_000,
      spent_micro: 0,
      reserved_micro: 0,
    },
  ],
  reservations: [{ model: "anthropic.claude-haiku", min_micro: 120_000, max_output_micro: 620_000 }],
  floor_micro: 120_000,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = async () => {
  await act(async () => {
    await Promise.resolve();
  });
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantProvidedRoutesView slug="acme" />);
  });
  await flush();
}
const click = async (el: HTMLElement) => {
  await act(async () => {
    el.click();
  });
  await flush();
  await flush();
};
const type = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const policy = () => host!.querySelector<HTMLElement>(".prov-policy")!;
const row = (user: string) => host!.querySelector<HTMLElement>(`[data-member="${user}"]`)!;
const btn = (scope: HTMLElement, text: string) =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === text)!;

beforeEach(() => {
  api.mockImplementation(() => Promise.resolve(VIEW()));
  apiJSON.mockResolvedValue({ warnings: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant provided routes", () => {
  it("draws nothing where the CP does not serve provided routes", async () => {
    api.mockResolvedValue({ error: { code: "http_404", message: "404 page not found" } });
    await mount();
    expect(api).toHaveBeenCalledWith(PATH);
    expect(host!.innerHTML).toBe("");
  });

  it("fails closed on any other refusal or a partial answer", async () => {
    for (const answer of [
      { error: { code: "no_tenant", message: "unknown tenant", status: 404 } },
      { error: { code: "forbidden", message: "tenant administrators only", status: 403 } },
      { granted: true, members: [] },
      // Well-formed at the top, broken inside: a reservation row without its amounts.
      { ...VIEW(), reservations: [{ model: "anthropic.claude-haiku" }] },
      { ...VIEW(), members: [{ ...VIEW().members[0], email: undefined }] },
    ]) {
      api.mockResolvedValue(answer);
      await mount();
      expect(host!.querySelector("[role=alert]")).not.toBeNull();
      expect(host!.querySelectorAll("input, button").length).toBe(0);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("shows the grant read-only and the month's spend", async () => {
    await mount();
    expect(host!.querySelector('[data-testid="prov-grant"]')!.textContent).toContain("≈ $100.00");
    expect(host!.textContent).toContain("2026-11");
    expect(host!.textContent).toContain("≈ $1.25");
    // The grant is the super_admin's: no control for it on the tenant page.
    expect(host!.querySelector(".prov-grant")).toBeNull();
  });

  it("names every value cell by its column, for the phone's card layout (#4376)", async () => {
    await mount();
    const heads = Array.from(host!.querySelectorAll(".prov-members thead th")).map((th) => th.textContent);
    for (const tr of Array.from(host!.querySelectorAll<HTMLElement>(".prov-members tbody tr"))) {
      const cells = Array.from(tr.children);
      expect(cells.length).toBe(heads.length);
      cells.forEach((td, i) => {
        // The name and the save button span the card; every other cell carries its heading,
        // which the phone layout shows beside the value once the header row is hidden.
        if (td.classList.contains("prov-member-name") || td.classList.contains("prov-member-save")) return;
        expect(td.getAttribute("data-label"), `column ${i}`).toBe(heads[i]);
      });
    }
  });

  it("reports unsaved edits to the page holding it", async () => {
    const seen: boolean[] = [];
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <DirtyScope onChange={(d) => seen.push(d)}>
          <TenantProvidedRoutesView slug="acme" />
        </DirtyScope>,
      );
    });
    await flush();
    expect(seen.at(-1)).toBe(false);
    await type(row(VIEW().members[0].user_key).querySelector<HTMLInputElement>('input[type="text"]')!, "12");
    await flush();
    expect(seen.at(-1)).toBe(true);
  });

  it("saves the policy as the CP's input shape", async () => {
    await mount();
    await click(btn(policy(), "許可したメンバーだけ"));
    await click(btn(policy(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/policy`, "PUT", {
      members_only: true,
      member_default_micro: 5_000_000,
    });
  });

  it("saves one member, an emptied budget going back to the default", async () => {
    apiJSON.mockResolvedValue({
      warnings: ["the member's monthly budget admits a call only while its output cap is small"],
    });
    await mount();
    await type(row("alice").querySelector<HTMLInputElement>('input[type="text"]')!, "");
    await click(btn(row("alice"), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/members`, "PUT", {
      membership_id: "M-a",
      granted: true,
      budget_micro: null,
    });
    expect(host!.querySelector(".prov-warnings")?.textContent).toContain("output cap is small");
  });

  it("keeps a refusal and the edits beside the row", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "invalid_input",
        message: "the member's monthly budget ≈$0.05 is below the smallest reservation of one call",
        status: 400,
      },
    });
    await mount();
    const input = row("bob").querySelector<HTMLInputElement>('input[type="text"]')!;
    await type(input, "0.05");
    await click(btn(row("bob"), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/members`, "PUT", {
      membership_id: "M-b",
      granted: false,
      budget_micro: 50_000,
    });
    const alert = row("bob").querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("Control Plane が受け付けませんでした");
    expect(alert.textContent).toContain("smallest reservation");
    expect(row("bob").querySelector<HTMLInputElement>('input[type="text"]')!.value).toBe("0.05");
  });

  it("refuses an amount that is not a number without sending it", async () => {
    await mount();
    await type(policy().querySelector<HTMLInputElement>("input")!, "five");
    await click(btn(policy(), "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(policy().querySelector("[role=alert]")!.textContent).toContain("ドル単位");
  });

  it("does not save over a value someone else changed since the load", async () => {
    await mount();
    await click(btn(policy(), "許可したメンバーだけ"));
    api.mockResolvedValue({ ...VIEW(), member_default_micro: 9_000_000 });
    await click(btn(policy(), "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(policy().querySelector("[role=alert]")!.textContent).toContain("ほかの人がこの規則を更新");
  });
});

// #4380 P-6: the tab says whether the tenant may use the route, its month against the pool and
// when the month resets, why a member at zero cannot, and that the restriction can stop the route
// whatever the budget. Not granted, the budgets stay editable.
describe("tenant provided routes, status and month", () => {
  const mountWith = async (view: unknown, ruleStops?: string) => {
    api.mockImplementation(() => Promise.resolve(view));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<TenantProvidedRoutesView slug="acme" ruleStops={ruleStops} />);
    });
    await flush();
  };
  const summary = () => host!.querySelector<HTMLElement>('[data-testid="prov-grant"]')!;

  it("shows the grant, the month against the pool with what is held, and the reset", async () => {
    await mountWith({ ...VIEW(), reserved_micro: 750_000 });
    expect(summary().querySelector('[data-state="granted"]')!.textContent).toContain("このテナントで使えます");
    expect(summary().textContent).toContain("テナント全体の月の枠: ≈ $100.00");
    const bar = summary().querySelector<HTMLMeterElement>(".prov-tenant-use meter")!;
    // Spent and held both count against the pool, as admission counts them.
    expect([bar.max, bar.value]).toEqual([100_000_000, 2_000_000]);
    expect(summary().textContent).toContain("≈ $100.00 のうち ≈ $1.25");
    expect(summary().textContent).toContain("実行中の呼び出しの取り置き: ≈ $0.75");
    // The month ends at 00:00 UTC on the 1st, shown in the viewer's time.
    expect(summary().textContent).toContain(`${fmtDateTime(new Date(Date.UTC(2026, 11, 1)))} にリセット`);
  });

  it("keeps the budgets editable under a not-granted banner, without a pool bar", async () => {
    await mountWith({ ...VIEW(), granted: false, ceiling_micro: null, spent_micro: 0 });
    const banner = summary().querySelector('[data-state="not-granted"]')!;
    expect(banner.textContent).toContain("まだ使えません");
    expect(banner.textContent).toContain("許可されたときから効きます");
    expect(summary().querySelector(".prov-tenant-use")).toBeNull();
    expect(policy().querySelector<HTMLInputElement>('input[type="text"]')!.disabled).toBe(false);
    expect(row("alice").querySelector<HTMLInputElement>('input[type="text"]')!.disabled).toBe(false);
    // No member's budget applies yet: no bar and no amount reads as theirs to use.
    expect(host!.querySelector(".prov-members meter")).toBeNull();
    expect(row("alice").querySelector('[data-label="今月の使用 / 予算"]')!.textContent).toBe("≈ $1.00");
    expect(row("bob").querySelector('[data-label="今月の使用 / 予算"]')!.textContent).toBe("—");
  });

  it("says the source is switched off or not registered, and lists nothing as usable", async () => {
    for (const [registered, state, text] of [
      [true, "source-off", "停止されている"],
      [false, "source-unregistered", "登録されていない"],
    ] as const) {
      await mountWith({ ...VIEW(), source_registered: registered, source_active: false });
      const banner = summary().querySelector(`[data-state="${state}"]`)!;
      expect(banner.textContent).toContain(text);
      expect(banner.textContent).toContain("配備の管理者がソースを有効にする必要があります");
      expect(summary().querySelector('[data-state="granted"]')).toBeNull();
      expect(summary().querySelector(".prov-tenant-use")).toBeNull();
      expect(summary().querySelector(".prov-reservations")).toBeNull();
      expect(row("alice").querySelector('[data-label="今月の使用 / 予算"]')!.textContent).toBe("≈ $1.00");
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("says a source that is off also needs the grant when the tenant is not granted", async () => {
    await mountWith({ ...VIEW(), granted: false, ceiling_micro: null, source_registered: true, source_active: false });
    const banner = summary().querySelector('[data-state="source-off"]')!;
    expect(banner.textContent).toContain("このテナントに許可する必要があります");
    expect(banner.textContent).not.toContain("有効になったときから");
  });

  it("draws a registered, active source as usable", async () => {
    await mountWith({ ...VIEW(), source_registered: true, source_active: true });
    expect(summary().querySelector('[data-state="granted"]')).not.toBeNull();
    expect(summary().querySelector(".prov-reservations")).not.toBeNull();
  });

  it("draws as before from a CP that does not send the source fields", async () => {
    await mountWith(VIEW());
    expect(summary().querySelector('[data-state="granted"]')).not.toBeNull();
    expect(summary().querySelector('[data-state^="source-"]')).toBeNull();
  });

  it("does not read a failed fetch as a switched-off source", async () => {
    await mountWith({ error: { code: "internal", message: "boom", status: 500 } });
    expect(host!.querySelector('[data-state^="source-"]')).toBeNull();
    expect(host!.querySelector("[role=alert]")).not.toBeNull();
  });

  it("says why a member whose budget is zero cannot use it", async () => {
    const v = VIEW();
    v.member_default_micro = 0;
    v.members[0] = { ...v.members[0], budget_micro: 0, effective_micro: 0 };
    v.members[1] = { ...v.members[1], effective_micro: 0 };
    await mountWith(v);
    expect(row("alice").textContent).toContain("予算が 0 のため使えません");
    // Everyone may use it (not "granted members only"), so bob's zero default is said too.
    expect(row("bob").textContent).toContain("予算が 0 のため使えません");
    expect(policy().textContent).toContain("既定の予算が 0 なので");
  });

  it("shows no budget for a member who is not granted under granted members only", async () => {
    await mountWith({ ...VIEW(), members_only: true });
    const use = row("bob").querySelector('[data-label="今月の使用 / 予算"]')!;
    expect(use.textContent).toBe("—");
    expect(use.querySelector("meter")).toBeNull();
  });

  it("keeps what a member no longer granted still holds visible", async () => {
    const v = { ...VIEW(), members_only: true };
    v.members[1] = { ...v.members[1], reserved_micro: 620_000 };
    await mountWith(v);
    expect(row("bob").querySelector('[data-label="今月の使用 / 予算"]')!.textContent).toBe(
      "実行中の呼び出しの取り置き: ≈ $0.62",
    );
  });

  it("reads the same amount aloud as the text shows", async () => {
    await mountWith({ ...VIEW(), reserved_micro: 750_000 });
    const bar = summary().querySelector<HTMLMeterElement>(".prov-tenant-use meter")!;
    expect(bar.getAttribute("aria-label")).toBe("≈ $100.00 のうち ≈ $1.25");
  });

  it("shows each member's month against their budget in one column", async () => {
    await mountWith(VIEW());
    const use = row("alice").querySelector('[data-label="今月の使用 / 予算"]')!;
    expect(use.textContent).toContain("≈ $20.00 のうち ≈ $1.00");
    expect(use.querySelector("meter")).not.toBeNull();
  });

  it("names who stops the route by the restriction, and nothing when it is allowed", async () => {
    await mountWith(VIEW(), "このテナント");
    expect(host!.querySelector('[data-testid="prov-rule-stops"]')!.textContent).toContain(
      "止めているのは: このテナント",
    );
    act(() => root?.unmount());
    host?.remove();
    await mountWith(VIEW());
    expect(host!.querySelector('[data-testid="prov-rule-stops"]')).toBeNull();
  });

  it("shows no reset for a month it cannot read", async () => {
    await mountWith({ ...VIEW(), month: "2026-13" });
    expect(summary().textContent).toContain("今月（2026-13・UTC）");
    expect(summary().textContent).not.toContain("リセット");
  });
});

// #4380 P-8: the grant to the tenant and its monthly pool are the super_admin's, edited on the
// tenant's own provided tab. A tenant administrator never gets the card (the CP refuses their
// grant write anyway).
describe("the super_admin's grant card", () => {
  const mountAs = async (isSuper: boolean, view: unknown = VIEW()) => {
    api.mockImplementation(() => Promise.resolve(view));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<TenantProvidedRoutesView slug="acme" isSuper={isSuper} />);
    });
    await flush();
  };
  const grant = () => host!.querySelector<HTMLElement>(".prov-grant")!;

  it("is not drawn for a tenant administrator", async () => {
    await mountAs(false);
    expect(host!.querySelector(".model-access-provided")).not.toBeNull();
    expect(host!.querySelector(".prov-grant")).toBeNull();
  });

  it("grants with a pool as the CP's input shape, after re-reading the stored grant", async () => {
    await mountAs(true, { ...VIEW(), granted: false, ceiling_micro: null });
    await click(grant().querySelector<HTMLInputElement>('input[type="checkbox"]')!);
    await type(grant().querySelector<HTMLInputElement>('input[type="text"]')!, "50");
    await click(btn(grant(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/grant`, "PUT", { granted: true, ceiling_micro: 50_000_000 });
  });

  it("keeps the CP's refusal and the edits", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "invalid_input",
        message: "granting the deployment's Bedrock route needs a monthly ceiling above zero for the tenant",
        status: 400,
      },
    });
    await mountAs(true, { ...VIEW(), granted: false, ceiling_micro: null });
    await click(grant().querySelector<HTMLInputElement>('input[type="checkbox"]')!);
    await click(btn(grant(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/grant`, "PUT", { granted: true, ceiling_micro: null });
    expect(grant().querySelector("[role=alert]")!.textContent).toContain("monthly ceiling above zero");
    expect(grant().querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(true);
  });

  it("does not save over a grant changed since the load", async () => {
    await mountAs(true);
    await type(grant().querySelector<HTMLInputElement>('input[type="text"]')!, "120");
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), ceiling_micro: 200_000_000 }));
    await click(btn(grant(), "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(grant().querySelector("[role=alert]")).not.toBeNull();
  });
});

describe("tenant provided routes follow the events stream", () => {
  const seenCalls = () => api.mock.calls.filter((c) => c[0] === PATH).length;
  const frame = async (rev: string) => {
    await act(async () => {
      noteProvidedRev({ rev });
    });
    await flush();
    await flush();
  };
  beforeEach(() => resetProvidedRev());

  it("reads again on the super_admin's digest, on its own baseline", async () => {
    const admin = async (rev: string) => {
      await act(async () => {
        noteProvidedAdminRev({ rev });
      });
      await flush();
      await flush();
    };
    await mount();
    expect(seenCalls()).toBe(1);
    await admin("a1"); // baseline
    await frame("r1"); // the tenant frame's baseline is separate
    expect(seenCalls()).toBe(1);
    await admin("a1");
    expect(seenCalls()).toBe(1);
    await admin("a2");
    expect(seenCalls()).toBe(2);
  });

  it("reads again when the revision moves, not on the baseline or a repeat", async () => {
    await mount();
    expect(seenCalls()).toBe(1);
    await frame("r1");
    await frame("r1");
    expect(seenCalls()).toBe(1);
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), spent_micro: 2_250_000 }));
    await frame("r2");
    expect(seenCalls()).toBe(2);
    expect(host!.textContent).toContain("≈ $2.25");
  });

  it("keeps what is drawn when the background read fails", async () => {
    await mount();
    await frame("r1");
    api.mockRejectedValue(new Error("down"));
    await frame("r2");
    expect(host!.querySelector(".prov-policy")).not.toBeNull();
    expect(host!.querySelector("[role=alert]")).toBeNull();
  });

  it("does not replace a draft being typed: the read is owed until the edits are gone (#4464)", async () => {
    await mount();
    const calls = seenCalls();
    await frame("r1");
    await type(policy().querySelector<HTMLInputElement>("input")!, "12");
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), spent_micro: 2_250_000 }));
    await frame("r2");
    expect(seenCalls()).toBe(calls);
    expect(policy().querySelector<HTMLInputElement>("input")!.value).toBe("12");
    // Saving leaves no draft: the owed read is paid.
    await click(btn(policy(), "保存"));
    expect(seenCalls()).toBeGreaterThan(calls);
  });

  it("a hidden view does not read on its own and pays one read when shown", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <TabbedViews
          label="t"
          prefix="t"
          views={[
            { key: "a", label: "A", node: <div>a</div> },
            { key: "b", label: "B", node: <TenantProvidedRoutesView slug="acme" /> },
          ]}
        />,
      );
    });
    await flush();
    const base = seenCalls();
    await frame("r1");
    await frame("r2");
    await frame("r3");
    expect(seenCalls()).toBe(base);
    await click(document.getElementById("t-tab-b") as HTMLElement);
    expect(seenCalls()).toBe(base + 1);
    await frame("r4");
    expect(seenCalls()).toBe(base + 2);
  });
});
