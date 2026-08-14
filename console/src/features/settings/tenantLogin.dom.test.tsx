// サインイン方法の登録簿（SignInMethodRegister・docs/61 §61.11.6）。
//
// 押さえるのは「承認がここで完結すること」だけ:
//   ① 承認待ちの行で「承認して有効化」を押すと、その行の tenant_slug で組んだ
//      POST .../tenants/{slug}/idp/{id}/status が飛ぶ（台帳は GET /api/admin/idp を
//      読むので、slug は行から拾うしかない — ここを取り違えると別テナントを触る）
//   ② 押したあと一覧を読み直す（1 回きりの fetch だと押した本人にだけ結果が見えない）
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

import { SignInMethodRegister } from "./tenantLogin.tsx";

const ROW = {
  id: "idp1",
  name: "entra",
  tenant_slug: "acme",
  issuer: "https://login.microsoftonline.com/guid/v2.0",
  client_id: "cid",
  trust: "issuer",
  allowed_domains: "@acme.co.jp",
  status: "pending",
  usable: false,
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<SignInMethodRegister />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const findButton = (label: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".allow-acts button")).find((b) =>
    (b.textContent || "").includes(label),
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
});

describe("SignInMethodRegister", () => {
  it("承認待ちの行から、その行のテナントへ承認を投げて読み直す", async () => {
    api.mockResolvedValueOnce({ providers: [ROW] });
    apiJSON.mockResolvedValue({});
    // 2 回目の GET（承認後の読み直し）は有効化された姿を返す。
    api.mockResolvedValueOnce({ providers: [{ ...ROW, status: "active", usable: true }] });
    await mount();
    expect(api).toHaveBeenCalledWith("api/admin/idp");

    const approve = findButton("承認して有効化");
    expect(approve).toBeTruthy();
    await act(async () => {
      approve!.click();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", {
      status: "active",
    });
    expect(api).toHaveBeenCalledTimes(2); // 承認したら読み直す
    // 承認済みの行は「停止する」に変わる（台帳は空にならず残る）。
    expect(findButton("承認して有効化")).toBeFalsy();
    expect(findButton("停止する")).toBeTruthy();
  });

  it("有効な行からは停止できる", async () => {
    api.mockResolvedValue({ providers: [{ ...ROW, status: "active", usable: true }] });
    apiJSON.mockResolvedValue({});
    await mount();
    const suspend = findButton("停止する");
    expect(suspend).toBeTruthy();
    await act(async () => {
      suspend!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/idp/idp1/status", "POST", {
      status: "suspended",
    });
  });
});
