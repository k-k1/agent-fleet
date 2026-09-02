// アカウントタブ（サインイン方法の紐づけ・docs/log/61 §61.16 + 決定 37）。
// 押さえるのは 2 点だけ:
//   ① 紐づけ済みの方式が読め、いまのセッションの方式にその印がつく
//      （どれで入っているか分からないと、足すべき方式も分からない）
//   ② 「足す」は Console の中で完結せず、CP の /oauth2/link へ provider と next を
//      載せて出ていく（紐づけの門は CP 側にあり、ここは入口でしかない）
//   ③ 解除は「外せるかどうか」をサーバの答え（removable）に従うだけ。UI は写しで、
//      残数や現セッションの判定をこちらで作り直さない（決定 14）
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
// 確認ダイアログはプロバイダ配下でしか使えないので、ここでは「はい」を返す関数に
// 差し替える。見たいのはボタンの出し分けと叩く先で、ダイアログの実装ではない。
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
      // いま使っている方式は外せない（サーバがそう答える）。
      removable: false,
      label_ja: "Google でサインイン",
      label_en: "Sign in with Google",
    },
    // ラベルの無い行（env から消えた方式・停止中のテナント行）は id で出す。
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

  // ★ 締め出しのガードはサーバが持つ。UI はその答え（removable）に従うだけで、
  // 残数や現セッションの判定をここで作り直さない — 作り直すと 2 つの規則になる。
  it("いま使っている方式の解除ボタンは押せない", async () => {
    await mount();
    const rows = Array.from(document.querySelectorAll(".account-methods tbody tr"));
    const btn = (i: number) => rows[i].querySelector<HTMLButtonElement>("button");
    expect(btn(0)!.disabled).toBe(true);
    expect(btn(0)!.title).toContain("先に別の方法");
    expect(btn(1)!.disabled).toBe(false);
  });

  it("解除は provider と subject をクエリに載せて DELETE する", async () => {
    await mount();
    const btn = document.querySelectorAll(".account-methods tbody tr")[1].querySelector("button")!;
    await act(async () => {
      btn.click();
    });
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const [path, method] = apiJSON.mock.calls[0];
    // ★ provider id は ":" を含むのでパスに載せない（クエリで渡す）。
    const url = new URL(String(path), "http://localhost");
    expect(url.pathname).toBe("/api/me/login-methods");
    expect(url.searchParams.get("provider")).toBe("t:sub:github");
    expect(url.searchParams.get("subject")).toBe("gh-1");
    expect(method).toBe("DELETE");
  });
});
