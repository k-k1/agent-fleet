// The account tab (linking sign-in methods, docs/log/61 §61.16 + decision 37).
// Only three things are pinned down here:
//   1. The linked methods are readable and the current session's method is marked (without
//      knowing which one you signed in with, you cannot tell which one to add).
//   2. "Add" does not complete inside the Console: it leaves for the CP's /oauth2/link with
//      provider and next (the linking gate is on the CP side; this is only the entrance).
//   3. Detaching just follows the server's answer (removable). The UI is a copy and does not
//      re-derive the remaining count or the current-session check (decision 14).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => "/" + p,
}));
// The confirm dialog only works under its provider, so replace it with a function that always
// answers yes. What matters here is which buttons appear and what they call, not the dialog.
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { AccountTab } from "./AccountTab.tsx";

const METHODS = {
  enabled: true,
  linked: [
    {
      provider: "google",
      subject: "g-1",
      email: "yamada@acme.co.jp",
      last_login_at: "2026-08-15T09:00:00Z",
      current: true,
      // The method in use cannot be detached (that is what the server answers).
      removable: false,
      label_ja: "Google でサインイン",
      label_en: "Sign in with Google",
    },
    // A row without a label (a method dropped from env, a suspended tenant's row) shows the id.
    { provider: "t:sub:github", subject: "gh-1", email: "yamada@acme.co.jp", removable: true },
  ],
  linkable: [{ provider: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft" }],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AccountTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  api.mockResolvedValue(METHODS);
  apiJSON.mockReset();
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("AccountTab", () => {
  it("lists the linked methods and marks the one currently in use", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/me/login-methods");
    const rows = Array.from(document.querySelectorAll(".account-methods tbody tr"));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("Google でサインイン");
    expect(rows[0].textContent).toContain("いま使用中");
    // No label means the id. Hiding the row itself would hide a method that appeared unnoticed.
    expect(rows[1].textContent).toContain("t:sub:github");
    expect(rows[1].textContent).not.toContain("いま使用中");
  });

  it("pressing a method leaves for the CP's /oauth2/link carrying provider and next", async () => {
    const assign = vi.fn();
    const original = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...original, pathname: "/", search: "?tenant=acme", assign },
    });
    try {
      await mount();
      const btn = Array.from(document.querySelectorAll<HTMLButtonElement>(".account-add button")).find((b) =>
        b.textContent?.includes("Microsoft"),
      );
      expect(btn).toBeTruthy();
      await act(async () => {
        btn!.click();
      });
      expect(assign).toHaveBeenCalledTimes(1);
      const url = new URL(assign.mock.calls[0][0], "http://localhost");
      expect(url.pathname).toBe("/oauth2/link");
      expect(url.searchParams.get("provider")).toBe("entra");
      expect(url.searchParams.get("next")).toBe("/?tenant=acme");
    } finally {
      Object.defineProperty(window, "location", { configurable: true, value: original });
    }
  });

  // The lock-out guard belongs to the server. The UI only follows its answer (removable) and
  // does not re-derive the remaining count or the current-session check: that would be a
  // second copy of the rule.
  it("the detach button of the method in use is not pressable", async () => {
    await mount();
    const rows = Array.from(document.querySelectorAll(".account-methods tbody tr"));
    const btn = (i: number) => rows[i].querySelector<HTMLButtonElement>("button");
    expect(btn(0)!.disabled).toBe(true);
    expect(btn(0)!.title).toContain("先に別の方法");
    expect(btn(1)!.disabled).toBe(false);
  });

  it("detaching sends DELETE with provider and subject in the query", async () => {
    await mount();
    const btn = document.querySelectorAll(".account-methods tbody tr")[1].querySelector("button")!;
    await act(async () => {
      btn.click();
    });
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const [path, method] = apiJSON.mock.calls[0];
    // A provider id contains ":", so it never goes on the path; it is passed in the query.
    const url = new URL(String(path), "http://localhost");
    expect(url.pathname).toBe("/api/me/login-methods");
    expect(url.searchParams.get("provider")).toBe("t:sub:github");
    expect(url.searchParams.get("subject")).toBe("gh-1");
    expect(method).toBe("DELETE");
  });
});
