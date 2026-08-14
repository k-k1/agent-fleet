// テナント設定モーダル（docs/61 の面を管理モーダルから移した先）。ここが守るのは
// 「画面の出し分けを、サーバが持つ権限より緩めない」ことなので、それだけを jsdom で押さえる:
//   ① 既定＝テナント管理者（super_admin: false）で「承認して有効化」を出さないこと
//      ★ テスト側で isSuper を作って渡すと、この既定の経路が無検証になる。だから
//        フラグは常に GET /api/admin/tenants のレスポンス由来にする（本番と同じ経路）。
//   ② super_admin: true が返ったときだけ承認が出ること（ハードコードで消していない）
//   ③ ログイン規則は読み取り専用（PUT が withSuperAdmin 固定・ADR0043 決定 19）
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

import { TenantDialog } from "./TenantDialog.tsx";
import { useSettingsUI } from "./store.ts";

const TENANT = {
  slug: "acme",
  name: "Acme",
  allowed_providers: "entra",
  auto_join_domains: "@sales.acme.co.jp",
  allowed_domains: "",
};
const IDP = {
  id: "idp1",
  name: "entra",
  issuer: "https://login.microsoftonline.com/guid/v2.0",
  client_id: "cid",
  trust: "issuer",
  allowed_domains: "@acme.co.jp",
  status: "pending",
  usable: false,
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function respond(superAdmin: boolean) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/tenants") {
      return Promise.resolve({ tenants: [TENANT], super_admin: superAdmin });
    }
    if (path.endsWith("/idp")) return Promise.resolve({ providers: [IDP] });
    return Promise.resolve({});
  });
}

async function mount(section: string) {
  useSettingsUI.getState().openTenantSettings(section);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantDialog />);
  });
  // テナント取得 → その slug で IdP 取得、の 2 段を流す。
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const buttonTexts = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".allow-acts button")).map(
    (b) => b.textContent || "",
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.getState().closeTenantSettings();
});

describe("TenantDialog", () => {
  it("テナント管理者には承認を出さない（既定＝super_admin: false）", async () => {
    respond(false);
    await mount("signin");
    expect(api).toHaveBeenCalledWith("api/admin/tenants");
    expect(api).toHaveBeenCalledWith("api/admin/tenants/acme/idp");
    // 編集・停止申請・削除はテナント管理者のもの。承認だけが出ない。
    const texts = buttonTexts();
    expect(texts.length).toBeGreaterThan(0);
    expect(texts.some((t) => t.includes("承認して有効化"))).toBe(false);
  });

  it("super_admin が返ったときだけ承認を出す", async () => {
    respond(true);
    await mount("signin");
    expect(buttonTexts().some((t) => t.includes("承認して有効化"))).toBe(true);
  });

  it("ログイン規則は読み取り専用で、未設定も値として読める", async () => {
    respond(false);
    await mount("rules");
    const content = document.querySelector(".settings-content")!;
    // 入力欄も保存ボタンも置かない（PUT は super_admin 固定なので、押せる顔を作らない）。
    expect(content.querySelector("input")).toBeNull();
    expect(content.querySelector(".admin-actions")).toBeNull();
    const vals = Array.from(content.querySelectorAll(".af-val")).map((e) => e.textContent || "");
    expect(vals).toHaveLength(3);
    expect(vals[0]).toBe("entra");
    expect(vals[1]).toBe("@sales.acme.co.jp");
    expect(vals[2]).toContain("未設定");
    // このテナント専用のログイン URL は規則の面にも出る（人が配る導線・決定 28）。
    expect(content.textContent).toContain("login/acme");
  });
});
