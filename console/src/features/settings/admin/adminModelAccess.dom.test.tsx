// Admin > Model access (ADR 0138 M2 P8). Pinned here:
//   1. The ceiling is read-only, with its source; the deployment rule and a tenant's cap are the
//      two editors, each PUT to its own path.
//   2. A rule above the ceiling is refused by the CP; the refusal is shown and the edits stay.
//   3. A refused read draws no editor (fail closed).
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

import { AdminModelAccessView } from "./adminModelAccess.tsx";

const CEILING = { raw: "classes=-subscription", rule: { classes: ["member-key", "member-cloud", "provided", "self-hosted"] }, source: "deployment" };
const DEPLOYMENT = { ceiling: CEILING, deployment: { rule: { destinations: ["anthropic", "aws-bedrock"] }, revision: 3 }, revision: 7 };
const tenantView = (slug: string) => ({
  ceiling: CEILING,
  deployment: DEPLOYMENT.deployment,
  cap: { rule: slug === "beta" ? { endpoints: ["*.beta.example"] } : {}, revision: 1 },
  tenant: { rule: {}, revision: 0 },
  restricted: true,
  revision: 7,
  routes: [{ id: "anthropic-api", class: "member-key", destination: "anthropic", allowed: true }],
  endpoints: [],
  endpointRoutes: [],
});
const TENANTS = [
  { slug: "acme", name: "Acme" },
  { slug: "beta", name: "Beta" },
];

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
    root!.render(<AdminModelAccessView tenants={TENANTS} />);
  });
  await flush();
  await flush();
}
const within = (sel: string) => host!.querySelector<HTMLElement>(sel)!;
const btnIn = (scope: HTMLElement, text: string) =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === text)!;
const tickIn = (scope: HTMLElement, field: string, text: string) =>
  Array.from(scope.querySelectorAll<HTMLLabelElement>(`[data-field="${field}"] label`)).find((l) =>
    l.textContent?.includes(text),
  )!.querySelector("input")!;
const click = async (el: HTMLElement) => {
  await act(async () => {
    el.click();
  });
  await flush();
};

beforeEach(() => {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/route-policy") return Promise.resolve(DEPLOYMENT);
    const m = /^api\/admin\/tenants\/([^/]+)\/route-policy$/.exec(path);
    if (m) return Promise.resolve(tenantView(m[1]));
    return Promise.resolve({ error: { code: "not_found" } });
  });
  apiJSON.mockResolvedValue({ rule: {}, revision: 9 });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("admin model access", () => {
  it("shows the ceiling read-only with its source", async () => {
    await mount();
    const c = within(".model-access-ceiling");
    expect(c.textContent).toContain("AF_MODEL_ROUTE_CEILING");
    expect(c.querySelector("code")?.textContent).toBe("classes=-subscription");
    expect(c.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("saves the deployment rule to its own path, keeping the fields not edited", async () => {
    await mount();
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    await click(btnIn(d, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/route-policy", "PUT", {
      destinations: ["anthropic", "aws-bedrock"],
      deny: ["anthropic-api"],
    });
  });

  it("shows the ceiling refusal and keeps the edits", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "route_policy_exceeds_ceiling",
        message: "the deployment's ceiling forbids routes this rule would allow: [anthropic-subscription]",
      },
    });
    await mount();
    const d = within(".model-access-deployment");
    // Widening the destinations back to all is what the ceiling's classes still forbid here.
    await click(btnIn(within('.model-access-deployment [data-field="destinations"]'), "すべて"));
    await click(btnIn(d, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/route-policy", "PUT", {});
    const alert = d.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("配備の上限が禁じているものを許す規則");
    expect(alert.textContent).toContain("anthropic-subscription");
    // Still "all": the edit was not thrown away.
    expect(
      btnIn(within('.model-access-deployment [data-field="destinations"]'), "すべて").getAttribute("aria-checked"),
    ).toBe("true");
    expect(api).toHaveBeenCalledTimes(2); // the deployment GET and the first tenant's, nothing reloaded
  });

  it("edits the picked tenant's cap and PUTs it to the cap path", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/route-policy");
    const sel = within(".model-access-caps select") as HTMLSelectElement;
    await act(async () => {
      sel.value = "beta";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    await flush();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/beta/route-policy");
    const cap = within('.mpol-cap[data-tenant="beta"]');
    expect(cap.querySelector("textarea")!.value).toBe("*.beta.example");
    await click(tickIn(cap, "deny", "anthropic-api"));
    await click(btnIn(cap, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/beta/route-policy/cap", "PUT", {
      endpoints: ["*.beta.example"],
      deny: ["anthropic-api"],
    });
  });

  it("draws no editor when the read is refused", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "super_admin only", status: 403 } });
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("super_admin only");
    expect(host!.querySelectorAll("input, textarea, button, select").length).toBe(0);
  });
});
