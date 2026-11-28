// The tenant default for open auto-runs: where its field sits and what a save sends.
//
// The Limits group is headed "0 = unlimited", and this field rejects 0, so it lives in its own
// group. The default is whatever the server reports; the Console carries no copy of it.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
const toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  getTenant: () => "",
  getUser: () => "",
  setTenant: () => {},
  setUser: () => {},
  isTransientErr: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantLimits } from "./tenantScope.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(tenant: object) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantLimits slug="acme" tenant={tenant as never} hasPool onChanged={() => {}} />);
  });
}

const field = () => host!.querySelector<HTMLInputElement>('input[type="number"][max="10"]')!;

async function save() {
  const btn = [...host!.querySelectorAll("button")].find((b) => b.className.includes("primary"));
  await act(async () => {
    btn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

async function type(el: HTMLInputElement, v: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant auto-run limit field", () => {
  it("sits in its own group, not under the '0 = unlimited' heading", async () => {
    await mount({ slug: "acme", memo_run_limit: 4 });
    const group = field().closest(".admin-fgroup")!;
    expect(group.querySelector("h4")?.textContent).toBe(t("admin.memo_run_group"));
    expect(group.textContent).not.toContain(t("admin.zero_unlimited"));
    // The unlimited fields stay under their heading.
    const limits = [...host!.querySelectorAll(".admin-fgroup")].find((g) => g.textContent?.includes(t("admin.zero_unlimited")))!;
    expect(limits.contains(field())).toBe(false);
    expect(limits.querySelectorAll("input").length).toBeGreaterThan(0);
  });

  it("shows the server's value and sends an edited one", async () => {
    apiJSON.mockResolvedValue({});
    await mount({ slug: "acme", memo_run_limit: 7 });
    expect(field().value).toBe("7");
    await type(field(), "9");
    await save();
    const [, , body] = apiJSON.mock.calls.find((c) => String(c[0]).includes("limits"))!;
    expect(body).toMatchObject({ memo_run_limit: 9 });
  });

  it("rejects 0 and values above 10", async () => {
    await mount({ slug: "acme", memo_run_limit: 4 });
    for (const v of ["0", "11"]) {
      await type(field(), v);
      await save();
    }
    expect(apiJSON).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(t("admin.memo_run_limit_range"));
  });

  it("does not invent a default when the server reported none, and leaves the field out of the save", async () => {
    apiJSON.mockResolvedValue({});
    await mount({ slug: "acme" });
    expect(field().value).toBe("");
    await save();
    const [, , body] = apiJSON.mock.calls.find((c) => String(c[0]).includes("limits"))!;
    // JSON drops an undefined member, so this is the field omitted on the wire.
    expect(JSON.parse(JSON.stringify(body))).not.toHaveProperty("memo_run_limit");
  });
});
