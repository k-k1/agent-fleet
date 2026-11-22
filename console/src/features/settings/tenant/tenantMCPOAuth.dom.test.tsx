// The tenant's MCP OAuth switch. Pinned: the panel shows what the server stores, a save PUTs
// {enabled}, and a JSON null body is handled as the unguarded `res.enabled` read always did —
// the reply is stored first, the read throws, and the caller's failure path runs.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));

import { TenantMCPOAuthView } from "./tenantMCPOAuth.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantMCPOAuthView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const check = () => document.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
const save = () => document.querySelector<HTMLButtonElement>("button.primary")!;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockResolvedValue({ enabled: true, served: true });
  apiJSON.mockResolvedValue({ enabled: false, served: true });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("tenant MCP OAuth switch", () => {
  it("shows the stored state and PUTs the change", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/mcp-oauth");
    expect(check().checked).toBe(true);
    await act(async () => check().click());
    await act(async () => save().click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/mcp-oauth", "PUT", { enabled: false });
  });

  it("hides the panel and toasts a failure when the save answers a JSON null", async () => {
    await mount();
    apiJSON.mockResolvedValue(null);
    await act(async () => check().click());
    await act(async () => save().click());
    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(t("tenant.mcp_oauth_save_failed"));
    expect(document.querySelector('input[type="checkbox"]')).toBeNull();
  });

  it("reports a failed load when the read answers a JSON null", async () => {
    api.mockResolvedValue(null);
    await mount();
    expect(document.querySelector('[role="alert"]')).not.toBeNull();
  });
});
