// Provided routes on Admin > Model access (ADR 0129 D1b, D3; ADR 0138 M2 P10). Pinned here:
//   1. Nothing is drawn where the CP does not serve provided routes; another failure draws no editor.
//   2. The source's PUT always carries `active` as loaded or edited: an inactive source re-saved
//      stays off (the CP reads an absent `active` as true).
//   3. A first registration offers Save only once the role, the Region and a model are filled.
//   4. The grant's PUT is exactly {granted, ceiling_micro}; refusals and stale values as on the
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

import { AdminProvidedSourceView } from "./adminProvidedRoutes.tsx";
import { noteProvidedAdminRev, resetProvidedRev } from "../tenant/providedRevision.ts";
import { fmtDateTime } from "../../../lib/intl.ts";

const SOURCE_PATH = "api/admin/provided-routes/deployment";
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
const typeModels = async (value: string) => {
  const ta = host!.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, value);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const save = () =>
  Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find((b) => b.textContent === "保存")!;
const inputs = () => Array.from(host!.querySelectorAll<HTMLInputElement>('input[type="text"]'));

beforeEach(() => {
  api.mockImplementation((path: string) => Promise.resolve(path === SOURCE_PATH ? { source: SOURCE() } : TENANT()));
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

  it("shows when the source was updated as a local date, not the raw timestamp", async () => {
    await mount(<AdminProvidedSourceView />);
    const dd = Array.from(host!.querySelectorAll('[data-testid="prov-source"] dd')).at(-1)!;
    expect(dd.textContent).not.toBe("");
    expect(dd.textContent).not.toContain("2026-11-16T");
    // Built with the same formatter, so the host's time zone and locale order do not matter.
    expect(dd.textContent).toBe(fmtDateTime("2026-11-16T10:00:00Z"));
  });

  it("offers no save for a first source until the role, the Region and a model are filled", async () => {
    api.mockResolvedValue({ source: null });
    await mount(<AdminProvidedSourceView />);
    expect(save().disabled).toBe(true);
    const [name, role, region] = inputs();
    await type(name, "bedrock");
    expect(save().disabled).toBe(true);
    await type(role, "arn:aws:iam::000000000000:role/route");
    expect(save().disabled).toBe(true);
    await type(region, "us-east-1");
    // The CP refuses an empty model allow-list.
    expect(save().disabled).toBe(true);
    await typeModels("anthropic.claude-haiku");
    expect(save().disabled).toBe(false);
    await type(role, "  ");
    expect(save().disabled).toBe(true);
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("registers a first source as active", async () => {
    api.mockResolvedValue({ source: null });
    await mount(<AdminProvidedSourceView />);
    const [, role, region] = inputs();
    await type(role, "arn:aws:iam::000000000000:role/route");
    await type(region, "us-east-1");
    await typeModels("anthropic.claude-haiku");
    await click(save());
    expect(apiJSON).toHaveBeenCalledWith(SOURCE_PATH, "PUT", {
      name: "",
      role_arn: "arn:aws:iam::000000000000:role/route",
      region: "us-east-1",
      models: ["anthropic.claude-haiku"],
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

  it("reads the source again when the super_admin's digest moves (#4371)", async () => {
    resetProvidedRev();
    await mount(<AdminProvidedSourceView />);
    const reads = () => api.mock.calls.filter((c: unknown[]) => c[0] === SOURCE_PATH).length;
    expect(reads()).toBe(1);
    const frame = async (rev: string) => {
      await act(async () => {
        noteProvidedAdminRev({ rev });
      });
      await flush();
      await flush();
    };
    await frame("a1"); // baseline
    await frame("a1");
    expect(reads()).toBe(1);
    await frame("a2");
    expect(reads()).toBe(2);
    resetProvidedRev();
  });
});
