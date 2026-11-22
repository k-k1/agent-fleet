// The development OIDC IdP switch (control-plane/dev_idp_admin.go). The server's answer drives
// everything on screen, so each state is a fixture: served, off, locked by the environment,
// and no PUBLIC_BASE_URL.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { t } from "../../../lib/i18n/index.ts";
import { DevIdpAdminView } from "./adminDevIdp.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const status = (over: Record<string, unknown> = {}) => ({
  enabled: true,
  setting: true,
  locked: false,
  missing_base_url: false,
  missing_preview_domain: false,
  issuer: "https://af.example.com/dev-idp",
  clients: 3,
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<DevIdpAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const box = () => host!.querySelector<HTMLInputElement>("#devidp-enabled")!;
const text = () => host!.textContent ?? "";

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

describe("DevIdpAdminView", () => {
  it("says members cannot use it without a preview domain, and keeps the switch operable", async () => {
    api.mockResolvedValue(status({ missing_preview_domain: true }));
    await mount();
    expect(text()).toContain(t("admin.devidp_no_preview_domain"));
    expect(box().disabled).toBe(false);
  });

  it("does not show the preview-domain line when one is configured", async () => {
    api.mockResolvedValue(status());
    await mount();
    expect(text()).not.toContain(t("admin.devidp_no_preview_domain"));
  });

  it("shows it on, with the issuer and the opted-in count", async () => {
    api.mockResolvedValue(status());
    await mount();
    expect(box().checked).toBe(true);
    expect(box().disabled).toBe(false);
    expect(text()).toContain("https://af.example.com/dev-idp");
    expect(text()).toContain("3");
    expect(host!.querySelector(".form-err")).not.toBeNull(); // the "never trust in production" line
  });

  it("PUTs the opposite of the box and draws the answer", async () => {
    api.mockResolvedValue(status());
    apiJSON.mockResolvedValue(status({ enabled: false, setting: false }));
    await mount();
    await act(async () => {
      box().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/dev-idp", "PUT", { enabled: false });
    expect(box().checked).toBe(false);
    expect(text()).not.toContain("https://af.example.com/dev-idp");
  });

  it("is locked, and says why, when AF_DEV_IDP=0", async () => {
    api.mockResolvedValue(status({ enabled: false, locked: true }));
    await mount();
    expect(box().disabled).toBe(true);
    expect(box().checked).toBe(false);
    expect(text()).toContain("AF_DEV_IDP=0");
  });

  it("cannot be switched on without PUBLIC_BASE_URL, and says why", async () => {
    api.mockResolvedValue(status({ enabled: false, missing_base_url: true, issuer: "" }));
    await mount();
    expect(box().disabled).toBe(true);
    expect(text()).toContain("PUBLIC_BASE_URL");
  });

  it("shows a refusal from the server", async () => {
    api.mockResolvedValue(status());
    apiJSON.mockResolvedValue({ error: { code: "forbidden", message: "super_admin required" } });
    await mount();
    await act(async () => {
      box().click();
    });
    expect(text()).toContain("super_admin required");
    expect(box().checked).toBe(true);
  });

  it("reports a lost response and re-reads the real state", async () => {
    api.mockResolvedValueOnce(status()).mockResolvedValueOnce(status({ enabled: false, setting: false }));
    apiJSON.mockRejectedValue(new TypeError("network"));
    await mount();
    await act(async () => {
      box().click();
    });
    expect(api).toHaveBeenCalledTimes(2); // the reload
    expect(box().checked).toBe(false); // the server had applied it
    expect(host!.querySelectorAll(".form-err").length).toBeGreaterThan(1); // warning + the error
    expect(box().disabled).toBe(false); // not stuck busy
  });

  it("does not show a stale state when the PUT and the re-read both fail", async () => {
    api.mockResolvedValueOnce(status()).mockRejectedValueOnce(new TypeError("network"));
    apiJSON.mockRejectedValue(new TypeError("network"));
    await mount();
    await act(async () => {
      box().click();
    });
    expect(box().disabled).toBe(true); // nothing to toggle: the state is unknown
    expect(box().checked).toBe(false);
    expect(text()).not.toContain("https://af.example.com/dev-idp");
    expect(text()).not.toContain("3");
    expect(host!.querySelectorAll(".form-err").length).toBeGreaterThan(1);
  });

  it("drops the old value when the PUT answers a null body, instead of keeping it live", async () => {
    api.mockResolvedValue(status());
    apiJSON.mockResolvedValue(null);
    await mount();
    await act(async () => {
      box().click();
    });
    expect(box().disabled).toBe(true);
    expect(text()).not.toContain("https://af.example.com/dev-idp");
  });
});
