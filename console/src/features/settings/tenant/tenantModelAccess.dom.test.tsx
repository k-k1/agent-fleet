// Tenant settings > Model access (ADR 0138 M2 P8). Pinned here:
//   1. The layers above are shown read-only and the tenant rule is the only editor.
//   2. A save PUTs the loaded rule with only the edited field changed: a field the page does not
//      edit (and one it does not even know) is sent back as loaded, so a write never widens by
//      dropping it.
//   3. A refusal is shown on the page, in the reader's language with the server's detail, and
//      the edits stay.
//   4. A refused or unreadable GET draws no editor at all (fail closed): this is what a member who
//      reaches the section by a direct link gets.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { readFileSync } from "node:fs";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { TenantModelAccessView } from "./tenantModelAccess.tsx";

const PATH = "api/admin/tenants/acme/route-policy";

const VIEW = () => ({
  ceiling: {
    raw: "classes=-subscription",
    rule: { classes: ["member-key", "member-cloud", "provided", "self-hosted"] },
    source: "deployment",
  },
  deployment: { rule: {}, revision: 0 },
  cap: { rule: { deny: ["openrouter-api"] }, revision: 2 },
  tenant: { rule: { endpoints: ["llm.corp.example"], except: ["copilot-login"], future: "kept" }, revision: 5 },
  restricted: true,
  revision: 9,
  routes: [
    { id: "anthropic-api", class: "member-key", destination: "anthropic", allowed: true },
    {
      id: "anthropic-subscription",
      class: "subscription",
      destination: "anthropic",
      allowed: false,
      forbiddenBy: ["ceiling"],
    },
    {
      id: "openrouter-api",
      class: "member-key",
      destination: "openrouter",
      allowed: false,
      forbiddenBy: ["tenant_cap"],
    },
    { id: "gemini-api", class: "member-key", destination: "google", allowed: false, forbiddenBy: ["tenant"] },
  ],
  endpoints: [{ host: "llm.corp.example", approved: false, excludedBy: ["ceiling"] }],
  endpointRoutes: [],
  hidden: { kinds: [], chat: [], imagegen: [] },
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantModelAccessView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const policyReads = () => api.mock.calls.filter((c) => c[0] === PATH).length;
const flush = async () => {
  await act(async () => {
    await Promise.resolve();
  });
};
const field = (name: string) => host!.querySelector<HTMLElement>(`.model-access-rule [data-field="${name}"]`)!;
const segBtn = (name: string, text: string) =>
  Array.from(field(name).querySelectorAll<HTMLButtonElement>(".seg-btn")).find((b) => b.textContent === text)!;
const tickIn = (name: string, text: string) =>
  Array.from(field(name).querySelectorAll<HTMLLabelElement>("label"))
    .find((l) => l.textContent?.includes(text))!
    .querySelector("input")!;
const button = (text: string) =>
  Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === text);
const click = async (el: HTMLElement) => {
  await act(async () => {
    el.click();
  });
  await flush();
};

beforeEach(() => {
  // The provided-route paths answer as on a CP without them (a plain 404): their section is
  // pinned in tenantProvidedRoutes.dom.test.tsx.
  api.mockImplementation((path: string) =>
    Promise.resolve(path.includes("/provided-routes/") ? { error: { code: "http_404", status: 404 } } : VIEW()),
  );
  apiJSON.mockResolvedValue({ rule: {}, revision: 6 });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant model access", () => {
  it("says who restricts what, one line per layer, read-only", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith(PATH);
    const who = host!.querySelector(".model-access-who")!;
    const lines = Array.from(who.querySelectorAll<HTMLElement>("[data-layer]"));
    expect(lines.map((l) => l.dataset.layer)).toEqual(["ceiling", "deployment", "tenant_cap", "tenant"]);
    // No control here: the layers above belong to the operator and the super_admin, and the
    // tenant's own is edited on the Restrictions tab.
    expect(who.querySelectorAll("input, textarea, button").length).toBe(0);
    expect(lines[0].textContent).toBe(
      "運用者（配備の設定・ここでは変更不可） 認証情報の種類 5 のうち 4（サブスクリプション・ログイン 以外）",
    );
    expect(lines[1].textContent).toContain("制限なし");
    expect(lines[2].textContent).toContain("個別に禁止: OpenRouter");
    // The tenant's own hosts, each with its state and who keeps it out.
    expect(host!.querySelector('[data-host="llm.corp.example"]')?.textContent).toContain("止めているのは: 運用者");
    // The sentences the policy page states: D12's delay and its window while the CP is
    // unreachable, and D13's.
    expect(host!.textContent).toContain("30 秒");
    expect(host!.textContent).toContain("Control Plane に届かない間");
    expect(host!.textContent).toContain("手作業でモデルにサインイン");
  });

  it("saves the loaded rule with only the edited field replaced", async () => {
    await mount();
    expect(button("保存")!.disabled).toBe(true);
    await click(segBtn("classes", "選んだものだけ"));
    await click(tickIn("classes", "メンバーの API キー"));
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON).toHaveBeenCalledWith(PATH, "PUT", {
      classes: ["member-key"],
      endpoints: ["llm.corp.example"],
      except: ["copilot-login"],
      future: "kept",
    });
    // One read to check the revision before the PUT, and the saved state read back after it.
    expect(policyReads()).toBe(3);
  });

  it("shows a refusal with its reason and keeps the edits", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "route_policy_endpoint_not_approvable",
        message: "gpu.corp.example cannot be approved as a self-hosted endpoint: excluded by [ceiling]",
      },
    });
    await mount();
    const ta = field("endpoints").querySelector("textarea")!;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      set.call(ta, "llm.corp.example\ngpu.corp.example");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledWith(
      PATH,
      "PUT",
      expect.objectContaining({ endpoints: ["llm.corp.example", "gpu.corp.example"] }),
    );
    const alert = host!.querySelector(".model-access-rule [role=alert]")!;
    expect(alert.textContent).toContain("セルフホストのエンドポイントとして承認できません");
    expect(alert.textContent).toContain("gpu.corp.example");
    expect(field("endpoints").querySelector("textarea")!.value).toBe("llm.corp.example\ngpu.corp.example");
    // Not reloaded after the refusal (the second read is the revision check before the PUT): a
    // reload would replace the edits with the stored rule.
    expect(policyReads()).toBe(2);
  });

  it("does not save over a revision someone else stored after the load", async () => {
    await mount();
    await click(tickIn("deny", "anthropic-api"));
    // Another administrator saved in the meantime: the stored tenant rule is now at revision 6.
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), tenant: { rule: {}, revision: 6 } }));
    await click(button("保存")!);
    expect(apiJSON).not.toHaveBeenCalled();
    expect(host!.querySelector(".model-access-rule [role=alert]")?.textContent).toContain("ほかの人がこの規則を更新");
    expect(tickIn("deny", "anthropic-api").checked).toBe(true);
  });

  it("does not save when the revision cannot be re-read", async () => {
    await mount();
    await click(tickIn("deny", "anthropic-api"));
    api.mockRejectedValue(new Error("offline"));
    await click(button("保存")!);
    expect(apiJSON).not.toHaveBeenCalled();
    expect(host!.querySelector(".model-access-rule [role=alert]")?.textContent).toContain("読み込めませんでした");
  });

  it("draws no editor when the CP refuses the read", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "tenant administrators only", status: 403 } });
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("tenant administrators only");
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("draws no editor when the read fails or answers a partial shape", async () => {
    api.mockRejectedValue(new Error("offline"));
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("読み込めませんでした");
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);

    act(() => root?.unmount());
    host?.remove();
    api.mockResolvedValue({ ceiling: VIEW().ceiling, routes: [] });
    await mount();
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });
});

// ADR 0138 M2 P9: what the policy hides, and from which layer. Fail closed: only a present,
// well-formed and empty `hidden` reads as "nothing is hidden".
// What members can use, on a CP without `owners`: only the hidden items can be named, with the
// guarantees of the list it replaces (#4368): every layer named, per route, and never "nothing is
// hidden" from a missing or partial answer.
describe("what members can use, from hidden alone", () => {
  const section = () => host!.querySelector(".model-access-use")!;
  const withHidden = (hidden: unknown, routes = VIEW().routes) =>
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), hidden, routes }));

  it("lists each hidden item with its routes and every layer that hides it", async () => {
    withHidden(
      {
        kinds: [{ id: "agy", routes: ["agy-login"], forbiddenBy: ["ceiling", "tenant"] }],
        imagegen: [{ id: "comfy", routes: ["af-engine-image"], forbiddenBy: ["deployment"] }],
        chat: [{ id: "codex", routes: ["chatgpt-subscription", "openai-api"], forbiddenBy: ["ceiling", "tenant_cap"] }],
      },
      [
        ...VIEW().routes,
        {
          id: "agy-login",
          class: "subscription",
          destination: "google",
          allowed: false,
          forbiddenBy: ["ceiling", "tenant"],
        },
        {
          id: "chatgpt-subscription",
          class: "subscription",
          destination: "openai",
          allowed: false,
          forbiddenBy: ["ceiling"],
        },
        { id: "openai-api", class: "member-key", destination: "openai", allowed: false, forbiddenBy: ["tenant_cap"] },
      ],
    );
    await mount();
    const agy = section().querySelector('[data-hidden="agy"]')!;
    // Hidden by two layers: both are named.
    expect(agy.textContent).toContain("止めているのは: 運用者・このテナント");
    expect(section().querySelector('[data-hidden="comfy"]')!.textContent).toContain("この配備のエンジン（ComfyUI）");
    expect(section().querySelector('[data-hidden="comfy"]')!.textContent).toContain("配備の管理者（全テナント共通）");
    // Each route carries its own layers, not only the item's union.
    const codex = section().querySelector('[data-hidden="codex"]')!;
    expect(codex.querySelector('[data-route="chatgpt-subscription"]')!.textContent).toContain("止めているのは: 運用者");
    expect(codex.querySelector('[data-route="chatgpt-subscription"]')!.textContent).not.toContain("このテナント向け");
    expect(codex.querySelector('[data-route="openai-api"]')!.textContent).toContain("配備の管理者（このテナント向け）");
    expect(section().querySelector('[data-testid="hidden-none"]')).toBeNull();
    // codex is hidden as a chat backend here, so its row sits in the chat list.
    expect(section().querySelector('[data-group="chat"] [data-hidden="codex"]')).not.toBeNull();
    // Only the hidden items can be named, and the page says why.
    expect(section().textContent).toContain("隠れているものだけ");
    // Decision 9: the members' own switches are not covered, and the page says so.
    expect(section().textContent).toContain("メンバー自身の切り替え");
  });

  it("shows an id outside the registry as itself, inherited object keys included", async () => {
    withHidden({
      kinds: [{ id: "toString", routes: ["agy-login"], forbiddenBy: ["ceiling"] }],
      chat: [],
      imagegen: [],
    });
    await mount();
    expect(section().querySelector('[data-hidden="toString"] strong')!.textContent).toBe("toString");
  });

  it("says nothing is hidden only when the list is present and empty", async () => {
    await mount();
    expect(section().querySelector('[data-testid="hidden-none"]')).not.toBeNull();
    expect(section().querySelector("[role=alert]")).toBeNull();
  });

  it("shows no list when hidden is missing or partial", async () => {
    for (const hidden of [undefined, { kinds: [] }, { kinds: [{ id: "agy" }], chat: [], imagegen: [] }]) {
      withHidden(hidden);
      await mount();
      expect(section().querySelector('[data-testid="hidden-none"]')).toBeNull();
      expect(section().querySelector("[data-hidden]")).toBeNull();
      expect(section().querySelector("[role=alert]")?.textContent).toContain("読めませんでした");
      act(() => root?.unmount());
      host?.remove();
      root = null;
      host = null;
    }
  });
});

// What members can use, from `owners` (#4380 P-4), on the CP's own answers (the shots fixtures,
// written by the real handlers): one row per agent, image provider and chat backend, its routes by
// name with the id on hover, and who stops each one.
describe("what members can use", () => {
  const fx = (name: string) =>
    JSON.parse(readFileSync(`scripts/shots/model-access/policy-${name}.json`, "utf8"))["tenant/demo"];
  const section = () => host!.querySelector(".model-access-use")!;
  const row = (group: string, id: string) =>
    section().querySelector<HTMLElement>(`[data-group="${group}"] [data-owner="${id}"]`)!;

  it("marks each agent usable or not, and names its routes and who stops them", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.includes("/provided-routes/") ? { error: { code: "http_404", status: 404 } } : fx("restricted"),
      ),
    );
    await mount();
    const claude = row("kinds", "claude");
    expect(claude.dataset.usable).toBe("true");
    expect(claude.querySelector("summary")!.textContent).toContain("4 の経路のうち 3 つで使えます");
    const sub = claude.querySelector('[data-route="anthropic-subscription"]')!;
    expect(sub.textContent).toContain("Claude サブスク");
    expect(sub.textContent).toContain("止めているのは: このテナント");
    // The route's id is on hover, not in the text.
    expect(sub.textContent).not.toContain("anthropic-subscription");
    expect(sub.querySelector("[title]")!.getAttribute("title")).toBe("anthropic-subscription");
    const cursor = row("kinds", "cursor");
    expect(cursor.dataset.usable).toBe("false");
    expect(cursor.querySelector("summary")!.textContent).toContain(
      "メンバーには見えません ― 止めているのは: 配備の管理者（このテナント向け）・このテナント",
    );
    // The same owner is judged per list: Claude Code's chat backend has only the subscription.
    expect(row("chat", "claude").dataset.usable).toBe("false");
    expect(row("imagegen", "comfy").dataset.usable).toBe("true");
    expect(section().textContent).toContain("メンバー自身の切り替え");
    expect(section().querySelector("[role=alert]")).toBeNull();
  });

  it("says a per-connection route lacks an approved server rather than blaming the layers", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.includes("/provided-routes/") ? { error: { code: "http_404", status: 404 } } : fx("unapproved"),
      ),
    );
    await mount();
    const route = row("kinds", "lcpp").querySelector('[data-route="member-llamacpp"]')!;
    expect(route.textContent).toContain("承認したセルフホストのサーバーがありません");
    expect(route.textContent).not.toContain("止めているのは");
    expect(row("kinds", "lcpp").querySelector("summary")!.textContent).toContain("2 の経路のうち 1 つで使えます");
  });

  it("lists everything as usable only from a well-formed owners", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path.includes("/provided-routes/") ? { error: { code: "http_404", status: 404 } } : fx("open")),
    );
    await mount();
    const rows = Array.from(section().querySelectorAll<HTMLElement>("[data-owner]"));
    expect(rows.length).toBe(20);
    expect(rows.every((r) => r.dataset.usable === "true")).toBe(true);
  });

  it("fails closed on a malformed owners, never falling back to hidden", async () => {
    const v = fx("open");
    for (const owners of [
      { ...v.owners, chat: undefined },
      { ...v.owners, kinds: [{ id: "claude", routes: ["anthropic-api"], allowed: ["bedrock"] }] },
    ]) {
      api.mockImplementation((path: string) =>
        Promise.resolve(
          path.includes("/provided-routes/") ? { error: { code: "http_404", status: 404 } } : { ...v, owners },
        ),
      );
      await mount();
      expect(section().querySelector("[data-owner]")).toBeNull();
      expect(section().querySelector('[data-testid="hidden-none"]')).toBeNull();
      expect(section().querySelector("[role=alert]")?.textContent).toContain("読めませんでした");
      act(() => root?.unmount());
      host?.remove();
      root = null;
      host = null;
    }
  });
});

// #4380 P-3: the page is split into views. Pinned here: every section is still reachable from a
// tab, D12's and D13's sentences stay on screen on every view, and the views stay mounted so a
// draft and a refusal survive a switch, with the tab of the unsaved view marked.
describe("model access views", () => {
  const PROVIDED = () => ({
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
  });
  const tab = (text: string) =>
    Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((t) => t.textContent?.startsWith(text));
  const shown = (el: Element) => el.closest("[hidden]") === null;

  it("reaches every section from a tab, and has no provided tab where the CP has none", async () => {
    await mount();
    expect(Array.from(host!.querySelectorAll('[role="tab"]')).map((t) => t.textContent)).toEqual(["概要", "制限"]);
    for (const sel of [".model-access-use", ".model-access-who", ".model-access-rule"]) {
      const panel = host!.querySelector(sel)!.closest<HTMLElement>('[role="tabpanel"]')!;
      const t = host!.querySelector<HTMLButtonElement>(`[role="tab"][aria-controls="${panel.id}"]`);
      expect(t, sel).not.toBeNull();
      await click(t!);
      expect(shown(host!.querySelector(sel)!), sel).toBe(true);
    }
  });

  it("gives provided routes their own tab where the CP serves them", async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.includes("/provided-routes/") ? PROVIDED() : VIEW()));
    await mount();
    await flush();
    const t = tab("提供モデルと予算")!;
    expect(shown(host!.querySelector(".model-access-provided")!)).toBe(false);
    await click(t);
    expect(shown(host!.querySelector(".model-access-provided")!)).toBe(true);
    expect(shown(host!.querySelector(".model-access-rule")!)).toBe(false);
  });

  it("does not carry one tenant's provided tab to the next before its own read answers", async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.includes("/provided-routes/") ? PROVIDED() : VIEW()));
    await mount();
    await flush();
    expect(tab("提供モデルと予算")).toBeDefined();
    // The next tenant's provided read never answers here.
    api.mockImplementation((path: string) =>
      path.includes("/provided-routes/") ? new Promise(() => {}) : Promise.resolve(VIEW()),
    );
    await act(async () => {
      root!.render(<TenantModelAccessView slug="beta" />);
    });
    await flush();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/beta/route-policy");
    expect(tab("提供モデルと予算")).toBeUndefined();
  });

  it("keeps D12's and D13's sentences on screen on every view", async () => {
    api.mockImplementation((path: string) => Promise.resolve(path.includes("/provided-routes/") ? PROVIDED() : VIEW()));
    await mount();
    await flush();
    const tabs = Array.from(host!.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
    expect(tabs.length).toBe(3);
    for (const t of tabs) {
      await click(t);
      for (const text of ["30 秒", "Control Plane に届かない間", "手作業でモデルにサインイン"]) {
        const p = Array.from(host!.querySelectorAll("p")).find((x) => x.textContent?.includes(text));
        expect(p && shown(p), `${t.textContent}: ${text}`).toBe(true);
      }
    }
  });

  it("keeps a draft and its refusal across a switch, and marks the unsaved tab", async () => {
    apiJSON.mockResolvedValue({
      error: { code: "route_policy_endpoint_not_approvable", message: "refused for the test" },
    });
    await mount();
    await click(tab("制限")!);
    expect(tab("制限")!.getAttribute("aria-label")).toBeNull();
    await click(segBtn("classes", "選んだものだけ"));
    await click(tickIn("classes", "メンバーの API キー"));
    expect(tab("制限")!.textContent).toBe("制限●");
    expect(tab("制限")!.getAttribute("aria-label")).toBe("制限, 未保存の変更あり");
    await click(button("保存")!);
    await flush();
    const refusal = () => host!.querySelector(".model-access-rule [role=alert]")?.textContent ?? "";
    expect(refusal()).toContain("refused for the test");

    await click(tab("概要")!);
    expect(shown(host!.querySelector(".model-access-rule")!)).toBe(false);
    await click(tab("制限")!);
    expect(refusal()).toContain("refused for the test");
    expect(tickIn("classes", "メンバーの API キー").checked).toBe(true);
    expect(tab("制限")!.textContent).toBe("制限●");
  });
});
