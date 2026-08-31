// テナントを削除する危険操作の置き場を固定する（docs/log/61 §61.18）。
//
// 押さえるのは 2 点:
//   ① 出るのは管理モーダル（onDeleted を渡した側）だけ。テナント設定モーダルは
//      自分のテナントの設定画面なので、そこにそのテナントを消すボタンは要らない。
//   ② 押した先が DELETE /api/admin/tenants/{slug} で、成功したら呼び出し側へ抜ける
//      （消えたテナントの中に留まらない）。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantScopeBody } from "./tenantScope.tsx";

const TENANT = { slug: "sales", name: "営業部", users: 0, running: 0 };

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(onDeleted?: () => void) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TenantScopeBody
        slug="sales"
        tenant={TENANT}
        section="limits"
        isSuper
        member={null}
        onOpenMember={() => {}}
        onCloseMember={() => {}}
        onChanged={() => {}}
        onDeleted={onDeleted}
      />,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const buttonWith = (text: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
    (b) => (b.textContent || "").trim() === text,
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue({});
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("テナントの削除", () => {
  it("onDeleted を渡さない面（テナント設定モーダル）には出さない", async () => {
    await mount();
    expect(buttonWith("テナントを削除")).toBeFalsy();
  });

  it("管理モーダルでは上限の節の末尾に出て、DELETE を投げてから抜ける", async () => {
    const onDeleted = vi.fn();
    await mount(onDeleted);

    await act(async () => buttonWith("テナントを削除")!.click());
    const confirm = buttonWith("削除する");
    expect(confirm).toBeTruthy();
    await act(async () => confirm!.click());

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/sales", "DELETE", {});
    expect(onDeleted).toHaveBeenCalled();
  });

  it("拒否されたら抜けない（先に片付ける操作が残っている）", async () => {
    const onDeleted = vi.fn();
    apiJSON.mockResolvedValue({ error: { code: "tenant_not_empty", message: "remove members first" } });
    await mount(onDeleted);

    await act(async () => buttonWith("テナントを削除")!.click());
    await act(async () => buttonWith("削除する")!.click());

    expect(onDeleted).not.toHaveBeenCalled();
  });
});
