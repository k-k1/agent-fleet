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

// The vocabulary the CP serves, as the real handlers wrote it (routepolicysrv TestShotFixturesPolicy).
const VOCAB = JSON.parse(readFileSync("scripts/shots/model-access/policy-open.json", "utf8"))["deployment"].vocabulary;

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
  vocabulary: VOCAB,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(isSuper = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantModelAccessView slug="acme" isSuper={isSuper} />);
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
// A route tick shows the route's name; its input's accessible name carries the id.
const tickIn = (name: string, text: string) =>
  Array.from(field(name).querySelectorAll<HTMLLabelElement>("label"))
    .find(
      (l) =>
        l.textContent?.includes(text) || l.querySelector("input")?.getAttribute("aria-label")?.endsWith(`: ${text}`),
    )!
    .querySelector("input")!;
const servers = () =>
  Array.from(field("endpoints").querySelectorAll("[data-server]")).map((r) => r.getAttribute("data-server"));
const addServer = async (host: string) => {
  const input = field("endpoints").querySelector<HTMLInputElement>(".mpol-server-add input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, host);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(
    Array.from(field("endpoints").querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === "追加")!,
  );
};
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
    await click(segBtn("classes", "選んだものだけ許す"));
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
    await addServer("gpu.corp.example");
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledWith(
      PATH,
      "PUT",
      expect.objectContaining({ endpoints: ["llm.corp.example", "gpu.corp.example"] }),
    );
    const alert = host!.querySelector(".model-access-rule [role=alert]")!;
    expect(alert.textContent).toContain("セルフホストのサーバーとして承認できません");
    expect(alert.textContent).toContain("gpu.corp.example");
    expect(servers()).toEqual(["llm.corp.example", "gpu.corp.example"]);
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

  it("says the Control Plane is older, and draws no editor, when the answer carries no vocabulary", async () => {
    const { vocabulary: _v, ...older } = VIEW();
    api.mockResolvedValue(older);
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("古く");
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("draws no editor when the vocabulary is empty", async () => {
    api.mockResolvedValue({ ...VIEW(), vocabulary: { classes: [], destinations: [], routes: [] } });
    await mount();
    expect(host!.querySelector("[role=alert]")?.textContent).toContain("読み込めませんでした");
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("offers what the CP serves, and shows an id the Console has no label for as itself", async () => {
    api.mockResolvedValue({
      ...VIEW(),
      vocabulary: {
        classes: ["subscription", "brand-new-class"],
        destinations: ["anthropic", "brand-new-dest"],
        routes: [{ id: "brand-new-route", class: "brand-new-class", destination: "brand-new-dest" }],
      },
    });
    await mount();
    await click(segBtn("classes", "選んだものだけ許す"));
    await click(segBtn("destinations", "選んだものだけ許す"));
    const text = host!.querySelector(".model-access-rule")!.textContent!;
    expect(text).toContain("brand-new-class");
    expect(text).toContain("brand-new-dest");
    expect(text).toContain("brand-new-route");
    // A route the CP no longer lists is not offered: the list is the CP's, not a copy here.
    expect(field("deny").textContent).not.toContain("openai-api");
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
// #4380 P-5a: the Restrictions tab's layout. The servers are rows, the individual allow/block
// lists fold into one row, routes are named (the id on hover), and each credential type says what
// it covers. What a save sends is unchanged: the loaded rule with only the edited fields over it.
describe("restrictions editor", () => {
  const rowsBtn = (host_: string) =>
    field("endpoints").querySelector<HTMLButtonElement>(`[data-server="${host_}"] button`)!;

  it("edits the servers as rows and sends only the endpoints changed", async () => {
    await mount();
    expect(servers()).toEqual(["llm.corp.example"]);
    await addServer("gpu.corp.example, gpu.corp.example");
    expect(servers()).toEqual(["llm.corp.example", "gpu.corp.example"]);
    await click(rowsBtn("llm.corp.example"));
    expect(servers()).toEqual(["gpu.corp.example"]);
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledWith(PATH, "PUT", {
      endpoints: ["gpu.corp.example"],
      except: ["copilot-login"],
      future: "kept",
    });
  });

  it("keeps a rule without endpoints without them after a server is added and removed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.includes("/provided-routes/")
          ? { error: { code: "http_404", status: 404 } }
          : { ...VIEW(), tenant: { rule: { deny: ["gemini-api"] }, revision: 5 } },
      ),
    );
    await mount();
    expect(servers()).toEqual([]);
    await addServer("llm.corp.example");
    expect(button("保存")!.disabled).toBe(false);
    await click(rowsBtn("llm.corp.example"));
    // Back to the loaded rule: absent, not [] (which would approve no host on purpose).
    expect(button("保存")!.disabled).toBe(true);
    await click(tickIn("deny", "openai-api"));
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledWith(PATH, "PUT", { deny: ["gemini-api", "openai-api"] });
  });

  it("draws a repeated stored host once, and keeps a repeat that adds nothing in the field", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path.includes("/provided-routes/")
          ? { error: { code: "http_404", status: 404 } }
          : { ...VIEW(), tenant: { rule: { endpoints: ["llm.corp.example", "llm.corp.example"] }, revision: 5 } },
      ),
    );
    await mount();
    expect(servers()).toEqual(["llm.corp.example"]);
    await addServer("llm.corp.example");
    expect(servers()).toEqual(["llm.corp.example"]);
    expect(field("endpoints").querySelector<HTMLInputElement>(".mpol-server-add input")!.value).toBe(
      "llm.corp.example",
    );
  });

  it("shows each stored server's state and marks a new one as not saved", async () => {
    await mount();
    await addServer("gpu.corp.example");
    const row = (h: string) => field("endpoints").querySelector(`[data-server="${h}"]`)!.textContent;
    expect(row("llm.corp.example")).toContain("承認されていません: 運用者の設定（変更不可）");
    expect(row("gpu.corp.example")).toContain("まだ保存していません");
  });

  it("folds the individual changes into one row that counts them", async () => {
    await mount();
    const fold = host!.querySelector<HTMLDetailsElement>('.model-access-rule [data-field="individual"]')!;
    expect(fold.tagName).toBe("DETAILS");
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")!.textContent).toBe("個別の変更: 1 件");
    await click(tickIn("deny", "anthropic-api"));
    expect(fold.querySelector("summary")!.textContent).toBe("個別の変更: 2 件");
  });

  it("names routes and keeps their ids on hover, and says what each credential type covers", async () => {
    await mount();
    const label = tickIn("except", "copilot-login").closest("label")!;
    expect(label.textContent).toContain("GitHub Copilot");
    expect(label.textContent).not.toContain("copilot-login");
    expect(label.querySelector("[title]")!.getAttribute("title")).toBe("copilot-login");
    await click(segBtn("classes", "選んだものだけ許す"));
    expect(tickIn("classes", "サブスクリプション・ログイン").closest("label")!.textContent).toContain(
      "Copilot、Cursor、Kiro",
    );
  });
});

// #4380 P-5b: presets fill in the draft, and options the layers above stop are marked. The
// tenant's own write is not checked against the ceiling, so nothing is locked here.
describe("restrictions help", () => {
  const preset = (key: string) => host!.querySelector<HTMLButtonElement>(`.model-access-rule [data-preset="${key}"]`)!;

  it("fills in the draft from a preset and marks it as the current one", async () => {
    await mount();
    expect(preset("none").getAttribute("aria-pressed")).toBe("false");
    await click(preset("no_subscriptions"));
    expect(preset("no_subscriptions").getAttribute("aria-pressed")).toBe("true");
    expect(preset("no_plan_logins").getAttribute("aria-pressed")).toBe("false");
    expect(tickIn("classes", "サブスクリプション・ログイン").checked).toBe(false);
    expect(tickIn("destinations", "opencode.ai").checked).toBe(false);
    // Nothing is sent until Save.
    expect(apiJSON).not.toHaveBeenCalled();
    await click(button("保存")!);
    expect(apiJSON).toHaveBeenCalledWith(PATH, "PUT", {
      endpoints: ["llm.corp.example"],
      future: "kept",
      classes: ["member-cloud", "member-key", "provided", "self-hosted"],
      destinations: [
        "af-engine",
        "anthropic",
        "aws-bedrock",
        "cursor",
        "github",
        "google",
        "kiro",
        "meta",
        "openai",
        "openrouter",
        "sakana",
        "self-hosted",
      ],
    });
  });

  it("marks what the layers above stop, and locks nothing on the tenant's own rule", async () => {
    await mount();
    await click(segBtn("classes", "選んだものだけ許す"));
    const sub = tickIn("classes", "サブスクリプション・ログイン").closest("label")!;
    expect(sub.querySelector(".mpol-mark")!.getAttribute("aria-label")).toBe(
      "止めているのは: 運用者の設定（変更不可）",
    );
    expect(sub.querySelector(".mpol-mark.locked")).toBeNull();
    expect(tickIn("classes", "メンバーの API キー").closest("label")!.querySelector(".mpol-mark")).toBeNull();
    expect(field("classes").textContent).toContain("上の制限が止めています");
    expect(host!.querySelector('.model-access-rule [data-testid="exceeds"]')).toBeNull();
  });
});

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

  it("says on the provided tab when the restriction stops the provided route, naming who", async () => {
    const stopped = {
      ...VIEW(),
      routes: [
        ...VIEW().routes,
        {
          id: "bedrock-deployment",
          class: "provided",
          destination: "aws-bedrock",
          allowed: false,
          forbiddenBy: ["tenant"],
        },
      ],
    };
    api.mockImplementation((path: string) =>
      Promise.resolve(path.includes("/provided-routes/") ? PROVIDED() : stopped),
    );
    await mount();
    await flush();
    expect(host!.querySelector('[data-testid="prov-rule-stops"]')!.textContent).toContain(
      "止めているのは: このテナント",
    );
    // And opens the Restrictions view, where it is changed.
    await click(tab("提供モデルと予算")!);
    await click(host!.querySelector<HTMLButtonElement>('[data-testid="prov-rule-stops"] button')!);
    expect(shown(host!.querySelector(".model-access-rule")!)).toBe(true);
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
    await click(segBtn("classes", "選んだものだけ許す"));
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

// #4380 P-8 (ADR 0138 D16, note of 2026-11-17): the deployment admin's restriction on a tenant (the
// cap) and the tenant's provided-route grant are edited on the tenant's own page, by the
// super_admin only. A tenant administrator never sees either card.
describe("the super_admin's cards", () => {
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
  const served = (view: unknown = VIEW()) =>
    api.mockImplementation((path: string) => Promise.resolve(path.includes("/provided-routes/") ? PROVIDED() : view));
  const cap = () => host!.querySelector<HTMLElement>("[data-cap-editor]")!;
  const capTick = (name: string, text: string) =>
    Array.from(cap().querySelectorAll<HTMLLabelElement>(`[data-field="${name}"] label`))
      .find(
        (l) =>
          l.textContent?.includes(text) || l.querySelector("input")?.getAttribute("aria-label")?.endsWith(`: ${text}`),
      )!
      .querySelector("input")!;
  const btnIn = (scope: Element, text: string) =>
    Array.from(scope.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === text)!;

  it("shows a tenant administrator neither the cap nor the grant", async () => {
    served();
    await mount(false);
    await flush();
    expect(host!.querySelector("[data-cap-editor]")).toBeNull();
    expect(host!.querySelectorAll(".model-access-rule .mpol-editor").length).toBe(1);
    expect(host!.querySelector(".model-access-provided")).not.toBeNull();
    expect(host!.querySelector(".prov-grant")).toBeNull();
  });

  it("gives the super_admin one cap editor, beside the tenant's own, and the grant card", async () => {
    served();
    await mount(true);
    await flush();
    expect(host!.querySelectorAll("[data-cap-editor]").length).toBe(1);
    expect(cap().closest('[role="tabpanel"]')).toBe(
      host!.querySelector(".model-access-rule")!.closest('[role="tabpanel"]'),
    );
    expect(host!.querySelectorAll(".prov-grant").length).toBe(1);
  });

  it("saves the cap to its own path after re-reading its revision, and refuses over a newer one", async () => {
    served({ ...VIEW(), ceiling: { raw: "", rule: {}, source: "deployment" } });
    await mount(true);
    await click(capTick("deny", "anthropic-api"));
    await click(btnIn(cap(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/cap`, "PUT", { deny: ["openrouter-api", "anthropic-api"] });
    apiJSON.mockClear();
    act(() => root?.unmount());
    host?.remove();
    served({ ...VIEW(), ceiling: { raw: "", rule: {}, source: "deployment" } });
    await mount(true);
    await click(capTick("deny", "anthropic-api"));
    api.mockImplementation(() => Promise.resolve({ ...VIEW(), cap: { rule: {}, revision: 3 } }));
    await click(btnIn(cap(), "保存"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(cap().querySelector("[role=alert]")?.textContent).toContain("ほかの人がこの規則を更新");
  });

  it("warns before a cap the CP would refuse, and matching it makes a save the CP accepts (#4376)", async () => {
    // The loaded cap only blocks OpenRouter: under a ceiling without subscriptions it still allows
    // them, which the CP refuses.
    served();
    await mount(true);
    const warn = cap().querySelector('[data-testid="exceeds"]')!;
    expect(warn.textContent).toContain("このままでは保存できません: 運用者の設定が");
    await click(btnIn(warn, "運用者の設定に合わせる"));
    expect(cap().querySelector('[data-testid="exceeds"]')).toBeNull();
    await click(btnIn(cap(), "保存"));
    expect(apiJSON).toHaveBeenCalledWith(`${PATH}/cap`, "PUT", {
      deny: ["openrouter-api"],
      classes: ["member-key", "member-cloud", "provided", "self-hosted"],
    });
  });

  it("locks what the ceiling forbids on the cap, and marks what the restriction for all tenants stops", async () => {
    served({ ...VIEW(), deployment: { rule: { destinations: ["anthropic", "aws-bedrock"] }, revision: 3 } });
    await mount(true);
    await click(btnIn(cap().querySelector('[data-field="classes"]')!, "選んだものだけ許す"));
    expect(
      capTick("classes", "サブスクリプション・ログイン").closest("label")!.querySelector(".mpol-mark.locked"),
    ).not.toBeNull();
    await click(btnIn(cap().querySelector('[data-field="destinations"]')!, "選んだものだけ許す"));
    const google = capTick("destinations", "Google").closest("label")!;
    expect(google.querySelector(".mpol-mark")!.getAttribute("aria-label")).toBe("止めているのは: 全テナント共通の制限");
    expect(google.querySelector(".mpol-mark.locked")).toBeNull();
  });
});
