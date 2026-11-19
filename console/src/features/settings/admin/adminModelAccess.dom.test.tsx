// Admin > Model access (ADR 0138 M2 P8; #4380 P-7, P-8). Pinned here:
//   1. The ceiling is read-only, with its source; the restriction for all tenants is the page's one
//      editor. A tenant's cap and grant are edited on that tenant's own page, never here.
//   2. A rule above the ceiling is refused by the CP; the refusal is shown and the edits stay.
//   3. A refused read draws no editor (fail closed).
import { readFileSync } from "node:fs";
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
import { RuleSummary } from "../tenant/routePolicyRuleEditor.tsx";

const CEILING = {
  raw: "classes=-subscription",
  rule: { classes: ["member-key", "member-cloud", "provided", "self-hosted"] },
  source: "deployment",
};
// The vocabulary the CP serves, as the real handlers wrote it (routepolicysrv TestShotFixturesPolicy).
const VOCAB = JSON.parse(
  readFileSync("scripts/shots/model-access/policy-open.json", "utf8"),
)["deployment"].vocabulary;
const DEPLOYMENT = {
  ceiling: CEILING,
  deployment: {
    rule: { destinations: ["anthropic", "aws-bedrock"] },
    revision: 3,
  },
  revision: 7,
  vocabulary: VOCAB,
};
const tenantView = (slug: string) => ({
  ceiling: CEILING,
  deployment: DEPLOYMENT.deployment,
  cap: {
    rule: slug === "beta" ? { endpoints: ["*.beta.example"] } : {},
    revision: 1,
  },
  tenant: { rule: {}, revision: 0 },
  restricted: true,
  revision: 7,
  routes: [
    {
      id: "anthropic-api",
      class: "member-key",
      destination: "anthropic",
      allowed: true,
    },
  ],
  endpoints: [],
  endpointRoutes: [],
  vocabulary: VOCAB,
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
async function mountNode(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await flush();
  await flush();
}
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
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find(
    (b) => b.textContent === text,
  )!;
// A route tick shows the route's name; its input's accessible name carries the id.
const tickIn = (scope: HTMLElement, field: string, text: string) =>
  Array.from(
    scope.querySelectorAll<HTMLLabelElement>(`[data-field="${field}"] label`),
  )
    .find(
      (l) =>
        l.textContent?.includes(text) ||
        l
          .querySelector("input")
          ?.getAttribute("aria-label")
          ?.endsWith(`: ${text}`),
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
    if (path.includes("provided-routes"))
      return Promise.resolve({ error: { code: "http_404", status: 404 } });
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
    // The operator's line carries no control: the setting cannot be changed here.
    expect(
      c
        .querySelector('[data-layer="ceiling"]')!
        .querySelectorAll("input, textarea, button").length,
    ).toBe(0);
  });

  it("pairs every label in a rule summary with its value, unrestricted layers included", async () => {
    // The grid lays the list out two cells per row, so one unpaired dt or dd shifts every row
    // below it onto the wrong value (#4376).
    for (const rule of [CEILING.rule, {}]) {
      await mountNode(<RuleSummary rule={rule} />);
      const dl = within("dl");
      const tags = Array.from(dl.children).map((c) => c.tagName);
      expect(tags.length % 2).toBe(0);
      tags.forEach((t, i) => expect(t).toBe(i % 2 === 0 ? "DT" : "DD"));
      expect(
        Array.from(dl.querySelectorAll("dd")).every(
          (dd) => dd.textContent !== "",
        ),
      ).toBe(true);
      const head = host!.querySelector(".mpol-summary-head");
      if (rule === CEILING.rule) expect(head).toBeNull();
      else
        expect([head?.textContent, head?.closest("dl")]).toEqual([
          "制限なし",
          null,
        ]);
      act(() => root?.unmount());
      host?.remove();
    }
    root = null;
    host = null;
  });

  it("names a rule summary's routes with their ids on hover", async () => {
    await mountNode(<RuleSummary rule={{ except: ["copilot-login"] }} />);
    const dd = Array.from(host!.querySelectorAll("dd")).find((d) =>
      d.querySelector('[title="copilot-login"]'),
    )!;
    expect(dd.textContent).toBe("GitHub Copilot");
  });

  it("saves the deployment rule to its own path, keeping the fields not edited", async () => {
    await mount();
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    await click(btnIn(d, "保存"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/route-policy",
      "PUT",
      { destinations: ["anthropic", "aws-bedrock"], deny: ["anthropic-api"] },
      { "If-Match": '"3"' },
    );
  });

  it("shows the ceiling refusal and keeps the edits", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "route_policy_exceeds_ceiling",
        message:
          "the deployment's ceiling forbids routes this rule would allow: [anthropic-subscription]",
      },
    });
    await mount();
    const d = within(".model-access-deployment");
    // Widening the destinations back to all is what the ceiling's classes still forbid here.
    await click(
      btnIn(
        within('.model-access-deployment [data-field="destinations"]'),
        "絞らない（今後増えるものも許す）",
      ),
    );
    await click(btnIn(d, "保存"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/route-policy",
      "PUT",
      {},
      { "If-Match": '"3"' },
    );
    const alert = d.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain(
      "運用者の設定が禁じているものを許す内容",
    );
    expect(alert.textContent).toContain("anthropic-subscription");
    // Still "all": the edit was not thrown away.
    expect(
      btnIn(
        within('.model-access-deployment [data-field="destinations"]'),
        "絞らない（今後増えるものも許す）",
      ).getAttribute("aria-checked"),
    ).toBe("true");
    // Only the deployment GET: nothing reloaded after the refusal (the per-tenant view reads
    // nothing until it is opened).
    expect(
      api.mock.calls.filter((c) => String(c[0]).endsWith("/route-policy"))
        .length,
    ).toBe(1);
  });

  it("shows the concurrent-edit message when the CP answers 409 to a stale revision", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "route_policy_stale",
        message: "at revision 4 now",
        status: 409,
      },
    });
    await mount();
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    await click(btnIn(d, "保存"));
    expect(d.querySelector("[role=alert]")?.textContent).toContain(
      "ほかの人がこの規則を更新",
    );
    expect(tickIn(d, "deny", "anthropic-api").checked).toBe(true);
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

  it("says the Control Plane is older, and draws no editor, when the answer carries no vocabulary", async () => {
    const { vocabulary: _v, ...older } = DEPLOYMENT;
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy" ? older : tenantView("acme"),
      ),
    );
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("古く");
    expect(
      host!.querySelectorAll("input, textarea, button, select").length,
    ).toBe(0);
  });

  it("draws no editor when the vocabulary is empty", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy"
          ? {
              ...DEPLOYMENT,
              vocabulary: { classes: [], destinations: [], routes: [] },
            }
          : tenantView("acme"),
      ),
    );
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain(
      "読み込めませんでした",
    );
    expect(
      host!.querySelectorAll("input, textarea, button, select").length,
    ).toBe(0);
  });

  it("offers the classes, destinations and routes the CP serves, ids it has no label for included", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/admin/route-policy"
          ? {
              ...DEPLOYMENT,
              vocabulary: {
                classes: ["subscription", "brand-new-class"],
                destinations: ["anthropic", "brand-new-dest"],
                routes: [
                  {
                    id: "brand-new-route",
                    class: "brand-new-class",
                    destination: "brand-new-dest",
                  },
                ],
              },
            }
          : tenantView("acme"),
      ),
    );
    await mount();
    const d = within(".model-access-deployment");
    for (const f of ["classes", "destinations"])
      await click(
        btnIn(
          d.querySelector<HTMLElement>(`[data-field="${f}"]`)!,
          "選んだものだけ許す",
        ),
      );
    const text = d.textContent!;
    expect(text).toContain("brand-new-class");
    expect(text).toContain("brand-new-dest");
    expect(text).toContain("brand-new-route");
    expect(text).not.toContain("copilot-login");
  });

  it("draws no editor when the read is refused", async () => {
    api.mockResolvedValue({
      error: { code: "forbidden", message: "super_admin only", status: 403 },
    });
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain(
      "super_admin only",
    );
    expect(
      host!.querySelectorAll("input, textarea, button, select").length,
    ).toBe(0);
  });
});

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
    Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find(
      (t) => t.textContent?.startsWith(text),
    );
  const shown = (el: Element) => el.closest("[hidden]") === null;

  it("reaches every section from a tab, with the provided tab only where the CP serves it", async () => {
    await mount();
    expect(
      Array.from(host!.querySelectorAll('[role="tab"]')).map(
        (t) => t.textContent,
      ),
    ).toEqual(["概要", "全テナント共通の制限", "テナントごと"]);
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
      ".model-access-provided-source",
    ]) {
      const panel = host!
        .querySelector(sel)!
        .closest<HTMLElement>('[role="tabpanel"]')!;
      const t = host!.querySelector<HTMLButtonElement>(
        `[role="tab"][aria-controls="${panel.id}"]`,
      );
      expect(t, sel).not.toBeNull();
      await click(t!);
      expect(shown(host!.querySelector(sel)!), sel).toBe(true);
    }
  });

  it("keeps D12's and D13's sentences on screen on every view", async () => {
    served();
    await mount();
    await flush();
    const tabs = Array.from(
      host!.querySelectorAll<HTMLButtonElement>('[role="tab"]'),
    );
    expect(tabs.length).toBe(4);
    for (const t of tabs) {
      await click(t);
      for (const text of [
        "30 秒",
        "Control Plane に届かない間",
        "手作業でモデルにサインイン",
      ]) {
        const p = Array.from(host!.querySelectorAll("p")).find((x) =>
          x.textContent?.includes(text),
        );
        expect(p && shown(p), `${t.textContent}: ${text}`).toBe(true);
      }
    }
  });

  it("keeps a draft and its refusal across a switch, and marks the unsaved tab", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "route_policy_exceeds_ceiling",
        message: "refused for the test",
      },
    });
    await mount();
    await click(tab("全テナント共通の制限")!);
    const d = within(".model-access-deployment");
    await click(tickIn(d, "deny", "anthropic-api"));
    expect(tab("全テナント共通の制限")!.textContent).toBe(
      "全テナント共通の制限●",
    );
    await click(btnIn(d, "保存"));
    await flush();
    expect(d.querySelector("[role=alert]")?.textContent).toContain(
      "refused for the test",
    );

    await click(tab("テナントごと")!);
    expect(shown(d)).toBe(false);
    await click(tab("全テナント共通の制限")!);
    expect(
      within(".model-access-deployment [role=alert]").textContent,
    ).toContain("refused for the test");
    expect(
      tickIn(within(".model-access-deployment"), "deny", "anthropic-api")
        .checked,
    ).toBe(true);
    expect(tab("全テナント共通の制限")!.textContent).toBe(
      "全テナント共通の制限●",
    );
    // The other views hold no edits.
    expect(tab("テナントごと")!.textContent).toBe("テナントごと");
  });
});

// #4380 P-5b, #4376 item 5: under the operator's ceiling the deployment rule and a cap lock what
// the ceiling forbids, warn before a save the CP would refuse, and can narrow the draft to fit.
describe("under the operator setting", () => {
  it("locks what the ceiling forbids on the restriction for all tenants", async () => {
    await mount();
    const d = within(".model-access-deployment");
    await click(
      btnIn(
        d.querySelector<HTMLElement>('[data-field="classes"]')!,
        "選んだものだけ許す",
      ),
    );
    const sub = tickIn(d, "classes", "サブスクリプション・ログイン").closest(
      "label",
    )!;
    expect(
      sub.querySelector(".mpol-mark.locked")!.getAttribute("aria-label"),
    ).toBe("運用者の設定が禁じています");
    expect(d.querySelector('[data-field="classes"]')!.textContent).toContain(
      "許したままでは保存できません",
    );
  });
});

// #4380 P-7, P-8: the overview says who decides what in plain lines and links to where each is
// changed; the per-tenant view lists every tenant, read only once that view is first opened, and
// opens each on its own Model access page.
describe("admin overview and tenants", () => {
  const SOURCE = (active = true) => ({
    source: {
      id: "S1",
      binding: "S1#fp1",
      name: "Shared Bedrock",
      role_arn: "arn:aws:iam::000000000000:role/route",
      region: "us-east-1",
      models: ["anthropic.claude-haiku"],
      fingerprint: "fp1",
      policy_version: 1,
      active,
      updated_at: "2026-11-16T10:00:00Z",
    },
  });
  const GRANT = (granted: boolean) => ({
    granted,
    ceiling_micro: granted ? 500_000_000 : null,
    members_only: false,
    member_default_micro: 0,
    month: "2026-11",
    spent_micro: granted ? 187_150_000 : 0,
    reserved_micro: 0,
    members: [],
    reservations: [],
    floor_micro: 0,
  });
  const serve = (source: unknown) => {
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) => {
      if (path === "api/admin/provided-routes/deployment")
        return Promise.resolve(source);
      const m =
        /^api\/admin\/tenants\/([^/]+)\/provided-routes\/deployment$/.exec(
          path,
        );
      if (m) return Promise.resolve(GRANT(m[1] === "acme"));
      return base(path);
    });
  };
  const tab = (text: string) =>
    Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find(
      (t) => t.textContent?.startsWith(text),
    )!;
  const shown = (el: Element) => el.closest("[hidden]") === null;
  const tenantReads = () =>
    api.mock.calls.filter((c) => /^api\/admin\/tenants\//.test(String(c[0])))
      .length;

  it("says who decides what in plain lines and opens the view where each is changed", async () => {
    serve(SOURCE());
    await mount();
    await flush();
    const who = within(".model-access-who");
    expect(
      who.querySelector('[data-layer="ceiling"] > span')!.textContent,
    ).toBe("認証情報の種類 5 のうち 4（サブスクリプション・ログイン 以外）");
    expect(
      who.querySelector('[data-layer="deployment"] > span')!.textContent,
    ).toContain("送り先 13 のうち 2: Anthropic・Amazon Bedrock");
    await click(
      who.querySelector<HTMLButtonElement>('[data-goto="deployment"]')!,
    );
    expect(shown(within(".model-access-deployment"))).toBe(true);
    await click(tab("概要"));
    expect(within('[data-testid="overview-source"]').textContent).toContain(
      "接続元: Shared Bedrock（us-east-1） 使用中",
    );
    await click(
      within(
        '[data-testid="overview-source"]',
      ).querySelector<HTMLButtonElement>('[data-goto="provided"]')!,
    );
    expect(shown(within(".model-access-provided-source"))).toBe(true);
  });

  it("says nothing of a source where the CP has none, and says when it could not be read", async () => {
    await mount();
    await flush();
    expect(host!.querySelector('[data-testid="overview-source"]')).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    serve({ error: { code: "internal", message: "boom", status: 500 } });
    await mount();
    await flush();
    expect(
      within('[data-testid="overview-source"] [role=alert]').textContent,
    ).toContain("boom");
  });

  it("reads the tenants only once the per-tenant view is opened, and opens a tenant's own page", async () => {
    serve(SOURCE(false));
    const onOpen = vi.fn();
    await mountNode(
      <AdminModelAccessView tenants={TENANTS} onOpenTenant={onOpen} />,
    );
    await flush();
    expect(within('[data-testid="overview-source"]').textContent).toContain(
      "停止中",
    );
    expect(tenantReads()).toBe(0);
    await click(tab("テナントごと"));
    await flush();
    // Each row reads the tenant's policy view and, where the CP serves them, its provided view.
    expect(tenantReads()).toBe(4);
    const row = (slug: string) =>
      within(`.mpol-tenants [data-tenant="${slug}"]`);
    expect(row("acme").querySelector('[data-col="cap"]')!.textContent).toBe(
      "制限なし",
    );
    expect(row("beta").querySelector('[data-col="cap"]')!.textContent).toBe(
      "承認してよいセルフホストのサーバー: *.beta.example",
    );
    expect(
      row("acme").querySelector('[data-col="provided"]')!.textContent,
    ).toBe("≈ $500.00 のうち ≈ $187.15");
    expect(
      row("beta").querySelector('[data-col="provided"]')!.textContent,
    ).toBe("未許可");
    await click(row("beta").querySelector("button")!);
    expect(onOpen).toHaveBeenCalledWith("beta");
  });

  it("asks before a tenant's Open drops an unsaved restriction for all tenants", async () => {
    const onOpen = vi.fn();
    const confirm = vi.spyOn(window, "confirm");
    await mountNode(
      <AdminModelAccessView tenants={TENANTS} onOpenTenant={onOpen} />,
    );
    await click(tab("テナントごと"));
    await flush();
    const open = () =>
      click(
        within('.mpol-tenants [data-tenant="beta"]').querySelector("button")!,
      );
    // Nothing unsaved: no question.
    await open();
    expect(confirm).not.toHaveBeenCalled();
    expect(onOpen).toHaveBeenCalledTimes(1);
    await click(tab("全テナント共通の制限"));
    await click(
      tickIn(within(".model-access-deployment"), "deny", "anthropic-api"),
    );
    await click(tab("テナントごと"));
    confirm.mockReturnValueOnce(false);
    await open();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledTimes(1);
    confirm.mockReturnValueOnce(true);
    await open();
    expect(onOpen).toHaveBeenCalledTimes(2);
    confirm.mockRestore();
  });

  it("names why a tenant's grant could not be read", async () => {
    serve(SOURCE());
    const base = api.getMockImplementation()!;
    api.mockImplementation((path: string) =>
      /tenants\/beta\/provided-routes/.test(path)
        ? Promise.resolve({
            error: {
              code: "internal",
              message: "grant read broke",
              status: 500,
            },
          })
        : base(path),
    );
    await mountNode(
      <AdminModelAccessView tenants={TENANTS} onOpenTenant={() => {}} />,
    );
    await click(tab("テナントごと"));
    await flush();
    expect(
      within('.mpol-tenants [data-tenant="beta"] [data-col="provided"]')
        .textContent,
    ).toContain("grant read broke");
  });

  it("edits caps and grants nowhere on this page: the restriction for all tenants is its one editor", async () => {
    serve(SOURCE());
    await mountNode(
      <AdminModelAccessView tenants={TENANTS} onOpenTenant={() => {}} />,
    );
    await flush();
    for (const t of [
      "概要",
      "全テナント共通の制限",
      "テナントごと",
      "提供モデル",
    ]) {
      await click(tab(t));
      await flush();
    }
    expect(host!.querySelectorAll(".mpol-editor").length).toBe(1);
    expect(
      within(".mpol-editor").closest(".model-access-deployment"),
    ).not.toBeNull();
    expect(host!.querySelector("[data-cap-editor], .prov-grant")).toBeNull();
  });

  it("moves the focus to the tab a link opens", async () => {
    serve(SOURCE());
    await mount();
    await flush();
    await click(
      within(".model-access-who").querySelector<HTMLButtonElement>(
        '[data-goto="deployment"]',
      )!,
    );
    expect(document.activeElement?.id).toBe("admin-mpol-tab-deployment");
  });

  it("holds the source line's place while it is read", async () => {
    serve(new Promise(() => {}));
    await mount();
    await flush();
    expect(within('[data-testid="overview-source"]').textContent).toContain(
      "読み込み中",
    );
  });

  it("reads twenty tenants at a time", async () => {
    serve(SOURCE());
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const many = Array.from({ length: 25 }, (_, i) => ({
      slug: `t${i}`,
      name: `T${i}`,
    }));
    await act(async () => {
      root!.render(<AdminModelAccessView tenants={many} />);
    });
    await flush();
    await flush();
    await click(tab("テナントごと"));
    await flush();
    expect(host!.querySelectorAll(".mpol-tenants tbody tr").length).toBe(20);
    await click(btnIn(within(".model-access-caps"), "さらに 5 件表示"));
    await flush();
    expect(host!.querySelectorAll(".mpol-tenants tbody tr").length).toBe(25);
  });
});
