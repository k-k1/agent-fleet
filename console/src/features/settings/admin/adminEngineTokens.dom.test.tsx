// The operator's Hugging Face and Civitai tokens (ADR 0072 decision 6 as revised, phase P5, and
// its Civitai counterpart). Each panel exists because the alternative was a CloudFormation round
// trip. What neither must do is imply it holds more than it does: the CP has `PutSecretValue`
// and no `GetSecretValue`, so there is no current value to show, and a field that looked like it
// had been pre-filled would be a lie the deployment cannot back.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ReactElement } from "react";

const api = vi.fn();
const apiJSON = vi.fn();
// Only the transport is stubbed. errDetail is the REAL one, because how a panel words a refusal
// is part of what is under test: a hand-written stub that echoed `message` back would have
// reported the English developer text as a pass.
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { HfTokenPanel, CivitaiTokenPanel } from "./adminEngineTokens.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(el: ReactElement) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(el);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const ui = () => document.body;

const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

// React tracks an input's value on the node, so assigning `.value` directly is invisible to
// it — the state stays empty and the button stays disabled, which looks exactly like a broken
// form.
const typeInto = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

const tokenInput = () => ui().querySelector('input[type="password"]') as HTMLInputElement | null;

const button = (label: string) =>
  Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
});

describe("HfTokenPanel", () => {
  const byPath = (hf: Record<string, unknown>) => (path: string) =>
    Promise.resolve(path === "api/admin/engines/hf-token" ? hf : {});

  it("registers a token and reports who and when, never the value", async () => {
    api.mockImplementation(byPath({ available: true, configured: false }));
    apiJSON.mockResolvedValue({
      available: true,
      configured: true,
      updated_by: "admin1",
      updated_at: "2026-09-09T12:00:00Z",
    });
    await mount(<HfTokenPanel />);

    const input = tokenInput()!;
    expect(input).toBeTruthy();
    expect(ui().textContent).toContain("未登録");
    // Empty is not a removal: the register button stays disabled until something is typed.
    expect(button("登録する")?.disabled).toBe(true);

    await typeInto(input, "hf_typed_value");
    await click(button("登録する"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/hf-token", "PUT", {
      token: "hf_typed_value",
    });
    expect(ui().textContent).toContain("登録済み");
    expect(ui().textContent).toContain("admin1");
    // 🔴 The token is not on the screen afterwards, in any field: the CP cannot read it back,
    // so anything the panel showed would be its own copy of a secret.
    expect(host!.innerHTML).not.toContain("hf_typed_value");
    expect(tokenInput()!.value).toBe("");
  });

  it("offers no field on a stack that keeps the token itself, and says gated still works", async () => {
    api.mockImplementation(byPath({ available: false, configured: true, stack_token: true }));
    await mount(<HfTokenPanel />);
    expect(tokenInput()).toBeNull();
    // Not "no token": this deployment HAS one, from a CloudFormation parameter. Reading as
    // "unregistered" would send somebody to fix what is not broken.
    expect(ui().textContent).toContain("CloudFormation");
    expect(ui().textContent).not.toContain("未登録");
  });

  it("says which half failed, in Japanese, when the secret refuses the write", async () => {
    api.mockImplementation(byPath({ available: true, configured: false }));
    apiJSON.mockResolvedValue({
      error: { code: "hf_token_put_failed", message: "AccessDeniedException: PutSecretValue" },
    });
    await mount(<HfTokenPanel />);
    const input = tokenInput()!;
    await typeInto(input, "hf_typed_value");
    await click(button("登録する"));

    const panel = ui().querySelector(".admin-panel")!;
    expect(panel.querySelector(".form-err")?.textContent).toContain("配備の秘密");
    // Still not registered, and the typed value is kept so it can be tried again rather than
    // retyped from wherever it came from.
    expect(ui().textContent).toContain("未登録");
    expect(tokenInput()!.value).toBe("hf_typed_value");
  });
});

// The Civitai token's own account is unrelated to the Hugging Face one — the same shape of
// panel, its own route, and (unlike Hugging Face) no CloudFormation-parameter legacy case at
// all: an "unavailable" deployment here simply has nowhere to register one yet.
describe("CivitaiTokenPanel", () => {
  const byPath = (civ: Record<string, unknown>) => (path: string) =>
    Promise.resolve(path === "api/admin/engines/civitai-token" ? civ : {});

  it("registers a token and reports who and when, never the value", async () => {
    api.mockImplementation(byPath({ available: true, configured: false }));
    apiJSON.mockResolvedValue({
      available: true,
      configured: true,
      updated_by: "admin1",
      updated_at: "2026-09-09T12:00:00Z",
    });
    await mount(<CivitaiTokenPanel />);

    const input = tokenInput()!;
    expect(input).toBeTruthy();
    expect(ui().textContent).toContain("未登録");

    await typeInto(input, "civitai_typed_value");
    await click(button("登録する"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/civitai-token", "PUT", {
      token: "civitai_typed_value",
    });
    expect(ui().textContent).toContain("登録済み");
    expect(ui().textContent).toContain("admin1");
    expect(host!.innerHTML).not.toContain("civitai_typed_value");
    expect(tokenInput()!.value).toBe("");
  });

  it("offers no field on a stack that has not been updated for it", async () => {
    api.mockImplementation(byPath({ available: false, configured: false }));
    await mount(<CivitaiTokenPanel />);
    expect(tokenInput()).toBeNull();
    expect(ui().textContent).toContain("60-engines");
  });

  it("removing it clears the panel and calls DELETE, not PUT with an empty token", async () => {
    api.mockImplementation(byPath({ available: true, configured: true, updated_by: "admin1" }));
    apiJSON.mockResolvedValue({ available: true, configured: false });
    await mount(<CivitaiTokenPanel />);
    expect(ui().textContent).toContain("登録済み");

    await click(button("削除する"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/civitai-token", "DELETE");
  });
});
