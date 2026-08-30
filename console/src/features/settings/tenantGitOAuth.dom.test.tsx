// テナントの git プロバイダ OAuth の面（docs/log/71・ADR 0052）。押さえるのは 3 点:
//   ① client_secret は書き込み専用。保存済みの値は返らないので、空のまま保存したら
//      「変えない」の意味で PUT する —— でないと client_id を直すたびに secret が
//      黙って消え、次の接続が invalid_client で落ちる。
//   ② secret を持たないプロバイダ（GitHub = デバイスフロー）には欄を出さない。
//      何を入れる欄か分からないものを置かない。
//   ③ コールバック URL を画面に出す。プロバイダ側の登録に貼るものなので、出さないと
//      管理者は登録を完了できない。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const raw = vi.fn();
const toast = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  raw: (...args: unknown[]) => raw(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

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

describe("git プロバイダ OAuth", () => {
  it("secret 欄は必要なプロバイダにだけ出し、コールバック URL を見せる", async () => {
    api.mockResolvedValue(LISTED);
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth");
    expect(groupFor("GitHub").querySelectorAll('input[type="password"]').length).toBe(0);
    expect(groupFor("Bitbucket").querySelectorAll('input[type="password"]').length).toBe(1);
    expect(document.body.textContent).toContain("https://af.example/api/oauth/bitbucket/callback");
  });

  it("secret を空のまま保存しても、保存済みの値を消しに行かない", async () => {
    api.mockResolvedValue(LISTED);
    apiJSON.mockResolvedValue({ provider: "bitbucket", client_id: "key-2", has_secret: true });
    await mount();
    const bb = groupFor("Bitbucket");
    await typeInto(bb.querySelector<HTMLInputElement>('input[type="text"]')!, "key-2");
    await act(async () => buttonIn(bb, "保存").click());
    // client_secret は空文字で送る = サーバ契約の「保存済みを保つ」。
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth/bitbucket", "PUT", {
      client_id: "key-2",
      client_secret: "",
    });
  });

  it("client_id が空なら保存させない（空の行を作らない）", async () => {
    api.mockResolvedValue({
      providers: [{ provider: "github", client_id: "", has_secret: false, needs_secret: false }],
    });
    await mount();
    expect(buttonIn(groupFor("GitHub"), "保存").disabled).toBe(true);
    // 未登録の行に「削除」は出さない。
    expect(
      Array.from(document.querySelectorAll("button")).some((b) => (b.textContent || "").includes("削除")),
    ).toBe(false);
  });

  it("コールバックが要るのに URL が無いとき、原因（PUBLIC_BASE_URL 未設定）を出す", async () => {
    // ★ ここで黙ると「登録したのに OAuth が失敗する」で止まる。直せるのは運用者で、
    //    テナント管理者が設定を入れ直しても何も変わらない。
    api.mockResolvedValue({
      providers: [{ provider: "bitbucket", client_id: "key-1", has_secret: true, needs_secret: true }],
    });
    await mount();
    expect(groupFor("Bitbucket").querySelector(".admin-hint.warn")).not.toBeNull();
    expect(document.body.textContent).toContain("PUBLIC_BASE_URL");
  });

  it("削除は DELETE を投げて読み直す", async () => {
    api.mockResolvedValue(LISTED);
    raw.mockResolvedValue({});
    await mount();
    api.mockClear();
    await act(async () => buttonIn(groupFor("GitHub"), "削除").click());
    expect(raw).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth/github", { method: "DELETE" });
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/git-oauth");
  });
});
