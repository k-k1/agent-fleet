// アカウントタブ（サインイン方法の紐づけ・docs/61 §61.16 + 決定 37）。
// 押さえるのは 2 点だけ:
//   ① 紐づけ済みの方式が読め、いまのセッションの方式にその印がつく
//      （どれで入っているか分からないと、足すべき方式も分からない）
//   ② 「足す」は Console の中で完結せず、CP の /oauth2/link へ provider と next を
//      載せて出ていく（紐づけの門は CP 側にあり、ここは入口でしかない）
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  rel: (p: string) => "/" + p,
}));

import { AccountTab } from "./AccountTab.tsx";

const METHODS = {
  enabled: true,
  linked: [
    {
      provider: "google",
      email: "yamada@acme.co.jp",
      last_login_at: "2026-08-15T09:00:00Z",
      current: true,
      label_ja: "Google でサインイン",
      label_en: "Sign in with Google",
    },
    // ラベルの無い行（env から消えた方式・停止中のテナント行）は id で出す。
    { provider: "t:sub:github", email: "yamada@acme.co.jp" },
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
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("AccountTab", () => {
  it("紐づけ済みの方式を並べ、いま使っている方式に印をつける", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/me/login-methods");
    const rows = Array.from(document.querySelectorAll(".account-methods tbody tr"));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("Google でサインイン");
    expect(rows[0].textContent).toContain("いま使用中");
    // ラベルが無ければ id。行そのものを隠すと「知らないうちに増えた方式」が見えなくなる。
    expect(rows[1].textContent).toContain("t:sub:github");
    expect(rows[1].textContent).not.toContain("いま使用中");
  });

  it("方式を押すと CP の /oauth2/link へ provider と next を載せて出ていく", async () => {
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
});
