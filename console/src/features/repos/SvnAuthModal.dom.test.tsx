// The way back from a checkout that did not save its credentials (docs/log/41 amendment).
//
// Two things are held here, and both are about not lying to the user:
//   - the server shown is the one the credential will be STORED under, read from the
//     working copy — nothing in this dialog invites typing a URL, because a typo there
//     would store a credential that matches nothing and fails silently forever;
//   - a rejected password stays in the dialog with the server's own verdict. The Agent
//     verifies before storing, so "saved" cannot come to mean "saved a typo" — the very
//     failure that sent the user here.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn(async (..._args: unknown[]) => ({}) as Record<string, unknown>);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: (path: string, method: string, body: unknown) => apiJSON(path, method, body),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  getTenant: () => "",
}));

import { SvnAuthModal } from "./SvnAuthModal.tsx";
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const saved: unknown[] = [];

const INFO = {
  url: "https://svn.example.com/proj/trunk",
  urlPrefix: "https://svn.example.com/proj",
  username: "",
  hasCred: false,
  trustCert: false,
};

async function render(onSaved?: () => void) {
  api.mockReset();
  api.mockImplementation(async () => INFO);
  host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host!);
    root.render(
      <ToastProvider>
        <SvnAuthModal repo="docs" onClose={() => {}} onSaved={onSaved} />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

// The modal renders through a portal under document.body, so anything scoped to `host`
// finds nothing and the assertion silently compares undefined.
const inputs = () => [...document.querySelectorAll("input")] as HTMLInputElement[];
const submit = () =>
  [...document.querySelectorAll("button")].find((b) => b.getAttribute("type") === "submit") as HTMLButtonElement;

async function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  saved.length = 0;
  apiJSON.mockReset();
  apiJSON.mockImplementation(async () => ({}));
});

describe("SvnAuthModal", () => {
  it("shows the server the credential will be stored under, and says none is saved yet", async () => {
    await render();
    const text = document.body.textContent || "";
    expect(text).toContain("https://svn.example.com/proj");
    expect(text).not.toContain("/proj/trunk"); // the prefix, not this folder's own URL
    expect(document.querySelector(".svn-auth-url")).toBeTruthy();
  });

  it("posts what was typed to the working copy's own endpoint", async () => {
    await render(() => saved.push("retried"));
    const [user, pass] = inputs();
    await type(user, "alice");
    await type(pass, "s3cret");
    await act(async () => {
      submit().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/repos/docs/svn-auth", "POST", {
      username: "alice",
      password: "s3cret",
      trustCert: false,
    });
    // onSaved is what retries the operation that failed — the dialog must not end in
    // "now try again".
    expect(saved).toEqual(["retried"]);
  });

  it("keeps a rejected credential in the dialog instead of reporting success", async () => {
    apiJSON.mockImplementation(async () => ({ error: { code: "svn_auth_required", message: "svn: E170001" } }));
    await render(() => saved.push("retried"));
    const [user, pass] = inputs();
    await type(user, "alice");
    await type(pass, "wrong");
    await act(async () => {
      submit().click();
    });
    expect(document.querySelector(".svn-auth-err")).toBeTruthy();
    expect(saved).toEqual([]);
  });
});
