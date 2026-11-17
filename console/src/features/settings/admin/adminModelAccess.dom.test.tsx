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

const CEILING = {
  raw: "classes=-subscription",
  rule: { classes: ["member-key", "member-cloud", "provided", "self-hosted"] },
  source: "deployment",
};
const DEPLOYMENT = {
  ceiling: CEILING,
  deployment: { rule: { destinations: ["anthropic", "aws-bedrock"] }, revision: 3 },
  revision: 7,
};
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
// A route tick shows the route's name; its input's accessible name carries the id.
const tickIn = (scope: HTMLElement, field: string, text: string) =>
  Array.from(scope.querySelectorAll<HTMLLabelElement>(`[data-field="${field}"] label`))
    .find(
      (l) =>
        l.textContent?.includes(text) || l.querySelector("input")?.getAttribute("aria-label")?.endsWith(`: ${text}`),
    )!
    .querySelector("input")!;
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
    // Provided routes off, as on a CP without them; pinned in adminProvidedRoutes.dom.test.tsx.
    if (path.includes("provided-routes")) return Promise.resolve({ error: { code: "http_404", status: 404 } });
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

  it("pairs every label in a rule summary with its value, unrestricted layers included", async () => {
    await mount();
    // The grid lays the list out two cells per row, so one unpaired dt or dd shifts every row
    // below it onto the wrong value (#4376). The ceiling restricts; the acme tenant's rule does not.
    const lists = [within(".model-access-ceiling dl"), within('.mpol-cap[data-tenant="acme"] dl')];
    for (const dl of lists) {
      const tags = Array.from(dl.children).map((c) => c.tagName);
      expect(tags.length % 2).toBe(0);
      tags.forEach((t, i) => expect(t).toBe(i % 2 === 0 ? "DT" : "DD"));
      expect(Array.from(dl.querySelectorAll("dd")).every((dd) => dd.textContent !== "")).toBe(true);
    }
    const head = within('.mpol-cap[data-tenant="acme"]').querySelector(".mpol-summary-head");
    expect(head?.textContent).toBe("制限なし");
    expect(head?.closest("dl")).toBeNull();
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
    await click(
      btnIn(within('.model-access-deployment [data-field="destinations"]'), "絞らない（今後増えるものも許す）"),
    );
    await click(btnIn(d, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/route-policy", "PUT", {});
    const alert = d.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("運用者の設定が禁じているものを許す内容");
    expect(alert.textContent).toContain("anthropic-subscription");
    // Still "all": the edit was not thrown away.
    expect(
      btnIn(
        within('.model-access-deployment [data-field="destinations"]'),
        "絞らない（今後増えるものも許す）",
      ).getAttribute("aria-checked"),
    ).toBe("true");
    // The deployment GET, the first tenant's, and the revision check before the PUT; nothing
    // reloaded after the refusal.
    expect(api.mock.calls.filter((c) => String(c[0]).endsWith("/route-policy")).length).toBe(3);
  });

  it("does not save the deployment rule over a newer revision", async () => {
    await mount();
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy"
          ? { ...DEPLOYMENT, deployment: { rule: {}, revision: 4 } }
          : tenantView("acme"),
      ),
    );
    await click(btnIn(d, "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(d.querySelector("[role=alert]")?.textContent).toContain("ほかの人がこの規則を更新");
  });

  it("does not save a cap over a newer revision", async () => {
    await mount();
    const cap = within('.mpol-cap[data-tenant="acme"]');
    await click(tickIn(cap, "deny", "anthropic-api"));
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy" ? DEPLOYMENT : { ...tenantView("acme"), cap: { rule: {}, revision: 2 } },
      ),
    );
    await click(btnIn(cap, "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(cap.querySelector("[role=alert]")?.textContent).toContain("ほかの人がこの規則を更新");
  });

  it("shows the ceiling's source as the wire names it", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy"
          ? { ...DEPLOYMENT, ceiling: { ...CEILING, source: "file" } }
          : tenantView("acme"),
      ),
    );
    await mount();
    const c = within(".model-access-ceiling");
    expect(c.textContent).toContain("file");
    expect(c.textContent).not.toContain("AF_MODEL_ROUTE_CEILING");
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
    expect(Array.from(cap.querySelectorAll("[data-server]")).map((r) => r.getAttribute("data-server"))).toEqual([
      "*.beta.example",
    ]);
    await click(tickIn(cap, "deny", "anthropic-api"));
    await click(btnIn(cap, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/beta/route-policy/cap", "PUT", {
      endpoints: ["*.beta.example"],
      deny: ["anthropic-api"],
    });
  });

  it("edits a cap's server patterns as rows and sends them as the CP's list", async () => {
    await mount();
    const cap = within('.mpol-cap[data-tenant="acme"]');
    const ep = cap.querySelector<HTMLElement>('[data-field="endpoints"]')!;
    // Loaded without endpoints: "no limit", no rows, no field to add to.
    expect(ep.querySelector(".mpol-server-add")).toBeNull();
    await click(btnIn(ep, "選んだものだけ許す"));
    const input = ep.querySelector<HTMLInputElement>(".mpol-server-add input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "*.acme.example");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(btnIn(ep, "追加"));
    expect(Array.from(ep.querySelectorAll("[data-server]")).map((r) => r.getAttribute("data-server"))).toEqual([
      "*.acme.example",
    ]);
    // Patterns carry no per-host state: that is the tenant page's.
    expect(ep.querySelector('[data-server="*.acme.example"]')!.textContent).not.toContain("まだ保存していません");
    await click(btnIn(cap, "保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/route-policy/cap", "PUT", {
      endpoints: ["*.acme.example"],
    });
  });

  it("sends an empty pattern list, not none, when every pattern row is removed", async () => {
    await mount();
    const sel = within(".model-access-caps select") as HTMLSelectElement;
    await act(async () => {
      sel.value = "beta";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    await flush();
    const cap = within('.mpol-cap[data-tenant="beta"]');
    await click(cap.querySelector<HTMLButtonElement>('[data-server="*.beta.example"] button')!);
    expect(cap.querySelectorAll("[data-server]").length).toBe(0);
    await click(btnIn(cap, "保存"));
    // "Only these" with no pattern approves no host: [] is a rule, unlike an absent list.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/beta/route-policy/cap", "PUT", { endpoints: [] });
  });

  it("names a rule summary's routes with their ids on hover", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/route-policy") return Promise.resolve(DEPLOYMENT);
      const slug = /tenants\/([^/]+)\/route-policy$/.exec(path)?.[1];
      if (slug)
        return Promise.resolve({ ...tenantView(slug), tenant: { rule: { except: ["copilot-login"] }, revision: 1 } });
      return Promise.resolve({ error: { code: "http_404", status: 404 } });
    });
    await mount();
    const dd = Array.from(within('.mpol-cap[data-tenant="acme"]').querySelectorAll("dd")).find((d) =>
      d.querySelector('[title="copilot-login"]'),
    )!;
    expect(dd.textContent).toBe("GitHub Copilot");
  });

  it("draws no editor when the read is refused", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "super_admin only", status: 403 } });
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("super_admin only");
    expect(host!.querySelectorAll("input, textarea, button, select").length).toBe(0);
  });
});

// ADR 0138 M2 P9: the hidden list follows the tenant picked for the cap.
describe("what the policy hides, per tenant", () => {
  it("shows the picked tenant's hidden list", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/route-policy") return Promise.resolve(DEPLOYMENT);
      const slug = /tenants\/([^/]+)\//.exec(path)?.[1] ?? "";
      const hidden =
        slug === "beta"
          ? { kinds: [{ id: "cursor", routes: ["cursor-login"], forbiddenBy: ["tenant_cap"] }], chat: [], imagegen: [] }
          : { kinds: [], chat: [], imagegen: [] };
      return Promise.resolve({ ...tenantView(slug), hidden });
    });
    await mount();
    expect(within('.mpol-cap[data-tenant="acme"] [data-testid="hidden-none"]')).not.toBeNull();
    const sel = within(".model-access-caps select") as HTMLSelectElement;
    await act(async () => {
      sel.value = "beta";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await flush();
    await flush();
    const item = within('.mpol-cap[data-tenant="beta"] [data-hidden="cursor"]');
    expect(item.textContent).toContain("配備の管理者からこのテナントへの制限");
  });

  it("shows no list when the tenant view carries none", async () => {
    await mount();
    // tenantView() has no `hidden`: not "nothing is hidden".
    expect(host!.querySelector('[data-testid="hidden-none"]')).toBeNull();
    expect(within(".model-access-hidden [role=alert]")).not.toBeNull();
  });
});

// #4380 P-3: the page is split into views (overview, restrictions for all tenants, per tenant,
// provided models). Pinned here: every section is reachable from a tab, D12's and D13's sentences
// stay on screen on every view, and a draft and its refusal survive a switch.
describe("admin model access views", () => {
  const SOURCE = {
    source: {
      id: "S1",
      binding: "S1#fp1",
      name: "deployment",
      role_arn: "arn:aws:iam::000000000000:role/route",
      region: "us-east-1",
      models: ["anthropic.claude-haiku"],
      fingerprint: "fp1",
      policy_version: 1,
      active: true,
      updated_at: "2026-11-16T10:00:00Z",
    },
  };
  const GRANT = {
    granted: false,
    ceiling_micro: null,
    members_only: false,
    member_default_micro: 0,
    month: "2026-11",
    spent_micro: 0,
    reserved_micro: 0,
    members: [],
    reservations: [],
    floor_micro: 0,
  };
  const served = () => {
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) =>
      path === "api/admin/provided-routes/deployment"
        ? Promise.resolve(SOURCE)
        : path.includes("/provided-routes/")
          ? Promise.resolve(GRANT)
          : base(path),
    );
  };
  const tab = (text: string) =>
    Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((t) => t.textContent?.startsWith(text));
  const shown = (el: Element) => el.closest("[hidden]") === null;

  it("reaches every section from a tab, with the provided tab only where the CP serves it", async () => {
    await mount();
    expect(Array.from(host!.querySelectorAll('[role="tab"]')).map((t) => t.textContent)).toEqual([
      "概要",
      "全テナント共通の制限",
      "テナントごと",
    ]);
    act(() => root?.unmount());
    host?.remove();
    served();
    await mount();
    await flush();
    expect(tab("提供モデル")).toBeDefined();
    for (const sel of [
      ".model-access-ceiling",
      ".model-access-deployment",
      ".model-access-caps",
      ".model-access-provided-grant",
      ".model-access-provided-source",
    ]) {
      const panel = host!.querySelector(sel)!.closest<HTMLElement>('[role="tabpanel"]')!;
      const t = host!.querySelector<HTMLButtonElement>(`[role="tab"][aria-controls="${panel.id}"]`);
      expect(t, sel).not.toBeNull();
      await click(t!);
      expect(shown(host!.querySelector(sel)!), sel).toBe(true);
    }
  });

  it("keeps D12's and D13's sentences on screen on every view", async () => {
    served();
    await mount();
    await flush();
    const tabs = Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
    expect(tabs.length).toBe(4);
    for (const t of tabs) {
      await click(t);
      for (const text of ["30 秒", "Control Plane に届かない間", "手作業でモデルにサインイン"]) {
        const p = Array.from(host!.querySelectorAll("p")).find((x) => x.textContent?.includes(text));
        expect(p && shown(p), `${t.textContent}: ${text}`).toBe(true);
      }
    }
  });

  it("keeps a draft and its refusal across a switch, and marks the unsaved tab", async () => {
    apiJSON.mockResolvedValue({ error: { code: "route_policy_exceeds_ceiling", message: "refused for the test" } });
    await mount();
    await click(tab("全テナント共通の制限")!);
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    expect(tab("全テナント共通の制限")!.textContent).toBe("全テナント共通の制限●");
    await click(btnIn(d, "保存"));
    await flush();
    expect(d.querySelector("[role=alert]")?.textContent).toContain("refused for the test");

    await click(tab("テナントごと")!);
    expect(shown(d)).toBe(false);
    await click(tab("全テナント共通の制限")!);
    expect(within(".model-access-deployment [role=alert]").textContent).toContain("refused for the test");
    expect(tickIn(within(".model-access-deployment"), "deny", "anthropic-api").checked).toBe(true);
    expect(tab("全テナント共通の制限")!.textContent).toBe("全テナント共通の制限●");
    // The other views hold no edits.
    expect(tab("テナントごと")!.textContent).toBe("テナントごと");
  });
});
