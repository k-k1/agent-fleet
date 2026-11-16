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

  it("saves the policy as the CP's input shape", async () => {
    await mount();
    await click(btn(policy(), "許可したメンバーだけ"));
    await click(btn(policy(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/policy`, "PUT", { members_only: true, member_default_micro: 5_000_000 });
  });

  it("saves one member, an emptied budget going back to the default", async () => {
    apiJSON.mockResolvedValue({ warnings: ["the member's monthly budget admits a call only while its output cap is small"] });
    await mount();
    await type(row("alice").querySelector<HTMLInputElement>('input[type="text"]')!, "");
    await click(btn(row("alice"), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/members`, "PUT", { membership_id: "M-a", granted: true, budget_micro: null });
    expect(host!.querySelector(".prov-warnings")?.textContent).toContain("output cap is small");
  });

  it("keeps a refusal and the edits beside the row", async () => {
    apiJSON.mockResolvedValue({
      error: { code: "invalid_input", message: "the member's monthly budget ≈$0.05 is below the smallest reservation of one call", status: 400 },
    });
    await mount();
    const input = row("bob").querySelector<HTMLInputElement>('input[type="text"]')!;
    await type(input, "0.05");
    await click(btn(row("bob"), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/members`, "PUT", { membership_id: "M-b", granted: false, budget_micro: 50_000 });
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
