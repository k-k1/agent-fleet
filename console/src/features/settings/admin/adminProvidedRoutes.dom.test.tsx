// Provided routes on Admin > Model access (ADR 0129 D1b, D3; ADR 0138 M2 P10). Pinned here:
//   1. Nothing is drawn where the CP does not serve provided routes; another failure draws no editor.
//   2. The source's PUT always carries `active` as loaded or edited: an inactive source re-saved
//      stays off (the CP reads an absent `active` as true).
//   3. The grant's PUT is exactly {granted, ceiling_micro}; refusals and stale values as on the
//      tenant page.
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

import { AdminProvidedGrantView, AdminProvidedSourceView } from "./adminProvidedRoutes.tsx";

const SOURCE_PATH = "api/admin/provided-routes/deployment";
const GRANT_PATH = "api/admin/tenants/acme/provided-routes/deployment";
const SOURCE = () => ({
  id: "S1",
  binding: "S1#fp1",
  name: "deployment",
  role_arn: "arn:aws:iam::000000000000:role/route",
  region: "us-east-1",
  models: ["anthropic.claude-haiku"],
  fingerprint: "fp1",
  policy_version: 1,
  active: false,
  updated_at: "2026-11-16T10:00:00Z",
});
const TENANT = () => ({
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

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = async () => {
  await act(async () => {
    await Promise.resolve();
  });
};
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
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
const save = () => Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === "保存")!;
const inputs = () => Array.from(host!.querySelectorAll<HTMLInputElement>('input[type="text"]'));

beforeEach(() => {
  api.mockImplementation((path: string) =>
    Promise.resolve(path === SOURCE_PATH ? { source: SOURCE() } : TENANT()),
  );
  apiJSON.mockResolvedValue({ warnings: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("admin provided-route source", () => {
  it("draws nothing where the CP does not serve provided routes", async () => {
    api.mockResolvedValue({ error: { code: "http_404", message: "404 page not found" } });
    await mount(<AdminProvidedSourceView />);
    expect(host!.innerHTML).toBe("");
  });

  it("draws no editor when the read is refused", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "super_admin only", status: 403 } });
    await mount(<AdminProvidedSourceView />);
    expect(host!.querySelector("[role=alert]")!.textContent).toContain("super_admin only");
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("draws no editor for a source without its binding", async () => {
    api.mockResolvedValue({ source: { ...SOURCE(), binding: undefined } });
    await mount(<AdminProvidedSourceView />);
    expect(host!.querySelector("[role=alert]")).not.toBeNull();
    expect(host!.querySelectorAll("input, textarea, button").length).toBe(0);
  });

  it("does not offer to save a model list that was only reordered", async () => {
    api.mockResolvedValue({ source: { ...SOURCE(), models: ["a.model", "b.model"] } });
    await mount(<AdminProvidedSourceView />);
    const ta = host!.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, "b.model, a.model");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(save().disabled).toBe(true);
  });

  it("keeps an inactive source off when it is re-saved", async () => {
    await mount(<AdminProvidedSourceView />);
    await type(inputs()[0], "bedrock");
    await click(save());
    expect(apiJSON).toHaveBeenCalledWith(SOURCE_PATH, "PUT", {
      name: "bedrock",
      role_arn: "arn:aws:iam::000000000000:role/route",
      region: "us-east-1",
      models: ["anthropic.claude-haiku"],
      active: false,
    });
  });

  it("registers a first source as active", async () => {
    api.mockResolvedValue({ source: null });
    await mount(<AdminProvidedSourceView />);
    const [, role, region] = inputs();
    await type(role, "arn:aws:iam::000000000000:role/route");
    await type(region, "us-east-1");
    await click(save());
    expect(apiJSON).toHaveBeenCalledWith(SOURCE_PATH, "PUT", {
      name: "",
      role_arn: "arn:aws:iam::000000000000:role/route",
      region: "us-east-1",
      models: [],
      active: true,
    });
  });

  it("does not save over a source changed since the load", async () => {
    await mount(<AdminProvidedSourceView />);
    await type(inputs()[0], "bedrock");
    api.mockResolvedValue({ source: { ...SOURCE(), updated_at: "2026-11-16T11:00:00Z" } });
    await click(save());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(host!.querySelector("[role=alert]")!.textContent).toContain("ほかの人がこの規則を更新");
  });
});

describe("admin provided-route grant", () => {
  it("draws nothing where the CP does not serve provided routes", async () => {
    api.mockResolvedValue({ error: { code: "http_404", message: "404 page not found" } });
    await mount(<AdminProvidedGrantView slug="acme" />);
    expect(host!.innerHTML).toBe("");
  });

  it("grants with a ceiling as the CP's input shape", async () => {
    await mount(<AdminProvidedGrantView slug="acme" />);
    await click(host!.querySelector<HTMLInputElement>('.prov-grant input[type="checkbox"]')!);
    await type(inputs()[0], "50");
    await click(save());
    expect(apiJSON).toHaveBeenCalledWith(`${GRANT_PATH}/grant`, "PUT", { granted: true, ceiling_micro: 50_000_000 });
  });

  it("keeps the CP's refusal and the edits", async () => {
    apiJSON.mockResolvedValue({
      error: {
        code: "invalid_input",
        message: "granting the deployment's Bedrock route needs a monthly ceiling above zero for the tenant",
        status: 400,
      },
    });
    await mount(<AdminProvidedGrantView slug="acme" />);
    await click(host!.querySelector<HTMLInputElement>('.prov-grant input[type="checkbox"]')!);
    await click(save());
    expect(apiJSON).toHaveBeenCalledWith(`${GRANT_PATH}/grant`, "PUT", { granted: true, ceiling_micro: null });
    expect(host!.querySelector("[role=alert]")!.textContent).toContain("monthly ceiling above zero");
    expect(host!.querySelector<HTMLInputElement>('.prov-grant input[type="checkbox"]')!.checked).toBe(true);
  });
});
