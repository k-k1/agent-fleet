// テナントの接続元制限の面（docs/log/66・ADR 0047）。押さえるのは 2 点:
//   ① 誤設定（プロキシ未申告）のときに保存ボタンを出さない。ここで「あなたの IP」
//      を出しただけの画面は、見えている ALB の私有アドレスをそのまま登録できて
//      しまう —— 絞ったつもりで全員を通す設定になる（決定 4）。
//   ② 締め出しの拒否はサーバの文言をそのまま出す。観測されたアドレスが書いて
//      なければ、管理者は打ち間違いとプロキシ問題を区別できない。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const toast = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantNetworkView } from "./tenantNetwork.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantNetworkView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const saveButton = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) =>
    (b.textContent || "").includes("保存"),
  );
const input = () => document.querySelector<HTMLInputElement>(".admin-fld input");

// React の制御された input は value プロパティを直接書いても onChange が走らない
// （React が setter を握っている）。ネイティブ setter 経由で書いてから input を投げる。
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
  toast.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  host?.remove();
  root = null;
  host = null;
});

describe("接続元の制限", () => {
  it("プロキシ未申告のときは編集させず、理由を出す", async () => {
    api.mockResolvedValue({
      allowed_cidrs: "",
      your_ip: "10.20.10.5",
      proxy_hops: 0,
      editable: false,
      reason: "proxy_not_configured",
    });
    await mount();
    expect(input()!.disabled).toBe(true);
    expect(saveButton()!.disabled).toBe(true);
    // 「気づかないまま登録できる」のを止めるのが目的なので、理由が読めること。
    expect(document.body.textContent).toContain("AF_TRUSTED_PROXY_HOPS");
  });

  it("保存でサーバに拒否されたら、その文言（観測アドレス入り）をそのまま出す", async () => {
    api.mockResolvedValue({ allowed_cidrs: "", your_ip: "198.51.100.7", editable: true, reason: "" });
    apiJSON.mockResolvedValue({
      error: { code: "would_lock_out", message: "your own address (198.51.100.7) is not in this list" },
    });
    await mount();
    await typeInto(input()!, "203.0.113.0/24");
    await act(async () => saveButton()!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/network", "PUT", {
      allowed_cidrs: "203.0.113.0/24",
    });
    expect(toast).toHaveBeenCalledWith("your own address (198.51.100.7) is not in this list");
  });

  it("保存が通ったら、正規化された結果を欄に書き戻す", async () => {
    // 保存後に読み直すので、2 回目の GET は保存済みの値を返す（実サーバと同じ）。
    api.mockResolvedValueOnce({ allowed_cidrs: "", your_ip: "203.0.113.9", editable: true, reason: "" });
    api.mockResolvedValue({ allowed_cidrs: "203.0.113.0/24", your_ip: "203.0.113.9", editable: true, reason: "" });
    apiJSON.mockResolvedValue({ tenant: "acme", allowed_cidrs: "203.0.113.0/24" });
    await mount();
    await typeInto(input()!, "203.0.113.9/24"); // ホスト部が残った書き方
    await act(async () => saveButton()!.click());
    // 打った文字ではなく保存された値が残る —— でないと、規則が言っていないことを
    // 言っていると信じたままになる。
    expect(input()!.value).toBe("203.0.113.0/24");
  });
});
