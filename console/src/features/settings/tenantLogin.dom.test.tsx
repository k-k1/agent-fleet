// ログイン面のうち「どこで何が読めるか」を固定する（docs/61 §61.11.6 / §61.11.8）。
// 前半は登録簿（SignInMethodRegister）、後半はログイン規則に添えるデプロイ共通の
// サインイン方法一覧。
//
// 登録簿で押さえるのは「承認がここで完結すること」だけ:
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

import { SignInMethodRegister, TenantLoginRules, TenantLoginRulesView } from "./tenantLogin.tsx";

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

async function mount(node: React.ReactNode = <SignInMethodRegister />) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
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

// 「使えるサインイン方法」欄に何が書けるか（docs/61 §61.11.8）。
//
// 押さえるのは 3 つ:
//   ① 編集できる面（管理モーダル）では、欄と同じパネルの中に id と表示名が並ぶ —
//      別の面に置くと、打ち間違えて 400 unknown_provider で弾かれた人が辿り着けない
//   ② 読み取り専用の面（テナント設定）では取りにいかない。ここに一覧を出すのは
//      P7-0 の統合リストの仕事で、この部品の仕事ではない
//   ③ ★ **「0 件」と「読めなかった」を混ぜない**（docs/61 §61.17.9 ②）。以前は
//      `res?.providers || []` でエラーを空配列に潰し、読めなかった相手に
//      「設定されていません」と嘘を表示していた。P7-0a で読める相手が広がるので、
//      ここを分けておかないと嘘が表に出る
describe("使えるサインイン方法の一覧", () => {
  const PROVIDERS = [
    { id: "google", label_ja: "Google でサインイン", label_en: "Sign in with Google", issuer: "https://accounts.google.com" },
    { id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft", issuer: "https://login.microsoftonline.com/guid/v2.0" },
  ];

  it("ログイン規則の編集面に、表示名と打ち込む id が並ぶ", async () => {
    api.mockResolvedValue({ providers: PROVIDERS });
    await mount(<TenantLoginRules slug="acme" tenant={{ allowed_providers: "entra" }} onChanged={() => {}} />);

    expect(api).toHaveBeenCalledWith("api/admin/providers");
    const rows = Array.from(host!.querySelectorAll(".idp-known .adm-mcp-row"));
    expect(rows).toHaveLength(2);
    // 表示名が主・id は <code>（技術識別子を主役にしない）。issuer は「どの Entra か」。
    expect(rows[1].querySelector(".as-name")?.textContent).toBe("Microsoft でサインイン");
    expect(rows[1].querySelector("code")?.textContent).toBe("entra");
    expect(rows[1].querySelector(".as-repo")?.textContent).toBe("https://login.microsoftonline.com/guid/v2.0");
  });

  it("1 つも無いデプロイでは、ボタンが出ないことを言う", async () => {
    api.mockResolvedValue({ providers: [] });
    await mount(<TenantLoginRules slug="acme" tenant={null} onChanged={() => {}} />);
    expect(host!.querySelectorAll(".idp-known .adm-mcp-row")).toHaveLength(0);
    expect(host!.querySelector(".idp-known .admin-hint")?.textContent).toContain("ボタンが出ません");
  });

  // ★ 403 は api() が {error:{code,message}} で返す（throw しない）。providers が
  // 配列でないことだけが「読めなかった」の判定材料になる。
  it("読めなかったときは「0 件」と言わない", async () => {
    api.mockResolvedValue({ error: { code: "forbidden", message: "tenant admin required" } });
    await mount(<TenantLoginRules slug="acme" tenant={null} onChanged={() => {}} />);
    const text = host!.querySelector(".idp-known")?.textContent ?? "";
    expect(text).not.toContain("ボタンが出ません");
    expect(text).toContain("読み込めませんでした");
  });

  it("通信断（reject）でも「0 件」と言わない", async () => {
    api.mockRejectedValue(new Error("network"));
    await mount(<TenantLoginRules slug="acme" tenant={null} onChanged={() => {}} />);
    const text = host!.querySelector(".idp-known")?.textContent ?? "";
    expect(text).not.toContain("ボタンが出ません");
    expect(text).toContain("読み込めませんでした");
  });

  // issuer は super_admin にしか返らない（§61.17.9 ①）。無いときは列ごと出さない。
  it("issuer が返らない相手には、空セルを作らない", async () => {
    api.mockResolvedValue({ providers: [{ id: "entra", label_ja: "Microsoft でサインイン", label_en: "Sign in with Microsoft" }] });
    await mount(<TenantLoginRules slug="acme" tenant={null} onChanged={() => {}} />);
    const rows = Array.from(host!.querySelectorAll(".idp-known .adm-mcp-row"));
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelector(".as-name")?.textContent).toBe("Microsoft でサインイン");
    expect(rows[0].querySelector(".as-repo")).toBeNull();
  });

  it("読み取り専用の面（テナント設定）は取りにいかない", async () => {
    api.mockResolvedValue({ providers: PROVIDERS });
    await mount(<TenantLoginRulesView slug="acme" tenant={{ allowed_providers: "entra" }} />);
    expect(api).not.toHaveBeenCalled();
    expect(host!.querySelector(".idp-known")).toBeNull();
  });
});
