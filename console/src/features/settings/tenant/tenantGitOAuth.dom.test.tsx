// The tenant git provider OAuth surface (docs/log/71, ADR 0052). Three things are pinned:
//   1. client_secret is write-only. The stored value is never returned, so saving with the
//      field empty PUTs it as "leave unchanged" — otherwise every edit of client_id would
//      silently wipe the secret and the next connection would fail with invalid_client.
//   2. Providers with no secret (GitHub, device flow) get no field at all. Never show a field
//      whose contents nobody can guess.
//   3. The callback URL is shown, because it is what the admin pastes into the provider's own
//      registration; without it the registration cannot be completed.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const raw = vi.fn();
const toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  raw: (...args: unknown[]) => raw(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantGitOAuthView } from "./tenantGitOAuth.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const LISTED = {
  providers: [
    { provider: "github", client_id: "Iv1.app", has_secret: false, needs_secret: false },
    {
      provider: "bitbucket",
      client_id: "key-1",
      has_secret: true,
      needs_secret: true,
      redirect_uri: "https://af.example/api/oauth/bitbucket/callback",
    },
  ],
};

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantGitOAuthView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const groups = () => Array.from(document.querySelectorAll<HTMLElement>(".admin-fgroup"));
const groupFor = (name: string) => groups().find((g) => (g.querySelector("h4")?.textContent || "").includes(name))!;
const buttonIn = (el: HTMLElement, text: string) =>
  Array.from(el.querySelectorAll<HTMLButtonElement>("button")).find((b) => (b.textContent || "").includes(text))!;

async function typeInto(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  apiJSON.mockReset();
  raw.mockReset();
  toast.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  host?.remove();
  root = null;
  host = null;
});

describe("git provider OAuth", () => {
  it("shows the secret field only where it is needed, and shows the callback URL", async () => {
    api.mockResolvedValue(LISTED);
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth");
    expect(groupFor("GitHub").querySelectorAll('input[type="password"]').length).toBe(0);
    expect(groupFor("Bitbucket").querySelectorAll('input[type="password"]').length).toBe(1);
    expect(document.body.textContent).toContain("https://af.example/api/oauth/bitbucket/callback");
  });

  it("does not clear the stored secret when saving with the field left empty", async () => {
    api.mockResolvedValue(LISTED);
    apiJSON.mockResolvedValue({ provider: "bitbucket", client_id: "key-2", has_secret: true });
    await mount();
    const bb = groupFor("Bitbucket");
    await typeInto(bb.querySelector<HTMLInputElement>('input[type="text"]')!, "key-2");
    await act(async () => buttonIn(bb, "保存").click());
    // Sending client_secret as the empty string is the server contract for "keep what is stored".
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth/bitbucket", "PUT", {
      client_id: "key-2",
      client_secret: "",
    });
  });

  it("blocks the save when client_id is empty, so no empty row is created", async () => {
    api.mockResolvedValue({
      providers: [{ provider: "github", client_id: "", has_secret: false, needs_secret: false }],
    });
    await mount();
    expect(buttonIn(groupFor("GitHub"), "保存").disabled).toBe(true);
    // An unregistered row gets no delete button (「削除」).
    expect(
      Array.from(document.querySelectorAll("button")).some((b) => (b.textContent || "").includes("削除")),
    ).toBe(false);
  });

  it("names the cause (PUBLIC_BASE_URL unset) when a callback is required but there is no URL", async () => {
    // Staying silent here leaves "registered it, but OAuth fails". Only the operator can fix
    // it; a tenant admin re-entering the settings changes nothing.
    api.mockResolvedValue({
      providers: [{ provider: "bitbucket", client_id: "key-1", has_secret: true, needs_secret: true }],
    });
    await mount();
    expect(groupFor("Bitbucket").querySelector(".admin-hint.warn")).not.toBeNull();
    expect(document.body.textContent).toContain("PUBLIC_BASE_URL");
  });

  it("sends DELETE and reloads on delete", async () => {
    api.mockResolvedValue(LISTED);
    raw.mockResolvedValue({});
    await mount();
    api.mockClear();
    await act(async () => buttonIn(groupFor("GitHub"), "削除").click());
    expect(raw).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth/github", { method: "DELETE" });
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth");
  });
});
