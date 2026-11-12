// The tenant's member defaults editor (ADR 0114 decision 8). Pinned:
//   1. The stored agent loads into the select, and a save sends the chosen one back.
//   2. A CP refusal is shown and the selection stays, so Save retries it.
//   3. A failed load says so and offers a reload.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { TenantMemberDefaultsPanel } from "./tenantMemberDefaults.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantMemberDefaultsPanel slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const select = () => document.querySelector<HTMLSelectElement>("select")!;
const button = (label: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) => (b.textContent || "").includes(label))!;
const alertText = () => document.querySelector('[role="alert"]')?.textContent || "";

async function choose(value: string) {
  await act(async () => {
    select().value = value;
    select().dispatchEvent(new Event("change", { bubbles: true }));
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("TenantMemberDefaultsPanel", () => {
  it("loads the stored agent and saves the chosen one", async () => {
    api.mockResolvedValue({ tenant: "acme", defaults: { agent: "codex" } });
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/member-defaults");
    expect(select().value).toBe("codex");

    apiJSON.mockResolvedValue({ tenant: "acme", defaults: { agent: "claude" }, updated_at: "2026-10-13T00:00:00Z" });
    await choose("claude");
    await act(async () => button(t("common.save")).click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/member-defaults", "PUT", {
      agent: "claude",
      launch: { agent: "", model: "", driver: "", permission_mode: "", budget_usd: null },
      repos: [],
      ops: { grafana_url: "", aws_sso_start_url: "", aws_account_id: "", aws_role_name: "" },
    });
    expect(select().value).toBe("claude");
  });

  it("shows a refusal and keeps the selection", async () => {
    api.mockResolvedValue({ tenant: "acme", defaults: { agent: "" } });
    await mount();
    apiJSON.mockResolvedValue({ error: { code: "invalid_defaults", message: "agent must be one of claude" } });
    await choose("kiro");
    await act(async () => button(t("common.save")).click());
    expect(alertText()).toContain("agent must be one of claude");
    expect(select().value).toBe("kiro");
  });

  it("offers a reload when the load fails", async () => {
    api.mockRejectedValueOnce(new Error("network down"));
    await mount();
    expect(alertText()).toContain("network down");
    api.mockResolvedValue({ tenant: "acme", defaults: { agent: "muse" } });
    await act(async () => button(t("tenant.member_defaults_retry")).click());
    await act(async () => {
      await Promise.resolve();
    });
    expect(select().value).toBe("muse");
  });
});

describe("TenantMemberDefaultsPanel budget field (C1)", () => {
  async function typeInto(input: HTMLInputElement, text: string) {
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
  const budget = () => document.querySelector<HTMLInputElement>('input[inputmode="decimal"]')!;

  it("keeps a half-typed decimal as typed and sends the final number", async () => {
    api.mockResolvedValue({ tenant: "acme", defaults: { agent: "" } });
    await mount();
    for (const t of ["1", "1.", "1.2", "1.25"]) {
      await typeInto(budget(), t);
      expect(budget().value).toBe(t);
    }
    apiJSON.mockResolvedValue({ tenant: "acme", defaults: { agent: "" } });
    await act(async () => button(t("common.save")).click());
    expect(apiJSON.mock.calls[0][2].launch.budget_usd).toBe(1.25);
  });

  it("refuses an unusable amount and sends nothing", async () => {
    api.mockResolvedValue({ tenant: "acme", defaults: { agent: "" } });
    await mount();
    await typeInto(budget(), "1.256");
    await act(async () => button(t("common.save")).click());
    expect(apiJSON).not.toHaveBeenCalled();
    expect(alertText()).toContain(t("tenant.member_defaults_budget_invalid"));
  });
});
