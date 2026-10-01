// The tenant branch rules editor (ADR 0103 decision 10). Pinned:
//   1. The stored list loads into the box and a save sends it back as a list under `rules`.
//   2. Text that is not a JSON list is stopped before the request, and a CP refusal is shown
//      verbatim with the text kept as typed, so the admin can fix the rule it names.
//   3. An empty box saves an empty list (removing every rule).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { TenantBranchRulesView } from "./tenantBranchRules.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantBranchRulesView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const box = () => document.querySelector<HTMLTextAreaElement>("textarea")!;
const saveButton = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) =>
    (b.textContent || "").includes("保存"),
  )!;
const alertText = () =>
  document.querySelector('[role="alert"]')?.textContent || "";

async function typeInto(el: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    "value",
  )!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function click(b: HTMLButtonElement) {
  await act(async () => {
    b.click();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const stored = [{ match: "*", base: "develop" }];

beforeEach(() => {
  (
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue({
    tenant: "acme",
    rules: stored,
    updated_at: "2026-10-01T09:00:00Z",
  });
});

afterEach(() => {
  act(() => root?.unmount());
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean })
    .IS_REACT_ACT_ENVIRONMENT;
  host?.remove();
  root = null;
  host = null;
});

describe("tenant branch rules", () => {
  it("loads the stored list and saves the edited one as a list", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/branch-rules");
    expect(JSON.parse(box().value)).toEqual(stored);

    const next = [{ match: "bitbucket.org/acme/*", name: "{prefix}{key}" }];
    apiJSON.mockResolvedValue({
      tenant: "acme",
      rules: next,
      updated_at: "2026-10-01T09:05:00Z",
    });
    await typeInto(box(), JSON.stringify(next));
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/tenants/acme/branch-rules",
      "PUT",
      { rules: next },
    );
    expect(JSON.parse(box().value)).toEqual(next);
    expect(alertText()).toBe("");
  });

  it("stops text that is not a JSON list before the request", async () => {
    await mount();
    await typeInto(box(), "{ not json");
    await click(saveButton());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(alertText()).toContain("JSON");

    await typeInto(box(), `{"match":"*"}`);
    await click(saveButton());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(alertText()).toContain("配列");
  });

  it("shows the CP's refusal verbatim and keeps the text as typed", async () => {
    await mount();
    const typed = `[{"match":"*","base":"a..b"}]`;
    apiJSON.mockResolvedValue({
      error: {
        code: "invalid_rule",
        message: 'rule 1: base "a..b" is not a branch name',
      },
    });
    await typeInto(box(), typed);
    await click(saveButton());
    expect(alertText()).toBe('rule 1: base "a..b" is not a branch name');
    expect(box().value).toBe(typed);
  });

  it("saves an empty box as no rules", async () => {
    await mount();
    apiJSON.mockResolvedValue({ tenant: "acme", rules: [] });
    await typeInto(box(), "  ");
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/tenants/acme/branch-rules",
      "PUT",
      { rules: [] },
    );
  });
});
