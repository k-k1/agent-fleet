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
  ceiling: { raw: "classes=-subscription", rule: { classes: ["member-key", "member-cloud", "provided", "self-hosted"] }, source: "deployment" },
  deployment: { rule: {}, revision: 0 },
  cap: { rule: { deny: ["openrouter-api"] }, revision: 2 },
  tenant: { rule: { endpoints: ["llm.corp.example"], except: ["copilot-login"], future: "kept" }, revision: 5 },
  restricted: true,
  revision: 9,
  routes: [
    { id: "anthropic-api", class: "member-key", destination: "anthropic", allowed: true },
    { id: "anthropic-subscription", class: "subscription", destination: "anthropic", allowed: false, forbiddenBy: ["ceiling"] },
    { id: "openrouter-api", class: "member-key", destination: "openrouter", allowed: false, forbiddenBy: ["tenant_cap"] },
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

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
  });
};
const field = (name: string) => host!.querySelector<HTMLElement>(`.model-access-rule [data-field="${name}"]`)!;
const segBtn = (name: string, text: string) =>
  Array.from(field(name).querySelectorAll<HTMLButtonElement>(".seg-btn")).find((b) => b.textContent === text)!;
const tickIn = (name: string, text: string) =>
  Array.from(field(name).querySelectorAll<HTMLLabelElement>("label")).find((l) => l.textContent?.includes(text))!
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
  api.mockImplementation(() => Promise.resolve(VIEW()));
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
  it("shows the layers above read-only and only the routes they leave", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith(PATH);
    const above = host!.querySelector(".model-access-above")!;
    expect(above.querySelectorAll("[data-layer]").length).toBe(3);
    // No control in the layers above: they belong to the operator and the super_admin.
    expect(above.querySelectorAll("input, textarea, button").length).toBe(0);
    const left = Array.from(host!.querySelectorAll('[data-testid="routes-above"] code')).map((c) => c.textContent);
    // The ceiling's and the cap's routes are gone; the one only the tenant forbids stays.
    expect(left).toEqual(["anthropic-api", "gemini-api"]);
    expect(host!.querySelector('[data-host="llm.corp.example"]')?.textContent).toContain("配備の上限（運用者）");
    // The two sentences the policy page states (D12, D13).
    expect(host!.textContent).toContain("30 秒");
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
    // The saved state is read back rather than assumed.
    expect(api).toHaveBeenCalledTimes(2);
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
    expect(apiJSON).toHaveBeenCalledWith(PATH, "PUT", expect.objectContaining({ endpoints: ["llm.corp.example", "gpu.corp.example"] }));
    const alert = host!.querySelector(".model-access-rule [role=alert]")!;
    expect(alert.textContent).toContain("セルフホストのエンドポイントとして承認できません");
    expect(alert.textContent).toContain("gpu.corp.example");
    expect(field("endpoints").querySelector("textarea")!.value).toBe("llm.corp.example\ngpu.corp.example");
    // Not reloaded: a reload would replace the edits with the stored rule.
    expect(api).toHaveBeenCalledTimes(1);
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
