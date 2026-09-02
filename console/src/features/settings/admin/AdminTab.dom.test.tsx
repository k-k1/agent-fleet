// 管理モーダルの左レール（ルート ↔ テナントのドリルダウン）。ここで押さえるのは
// IA そのもの——「レールが 2 段になっていて、テナントを開くとそのテナントの節に
// 入れ替わり、出口から一覧へ戻れる」こと。旧実装（横一列のモードタブ＋本文の
// パンくず）に戻ると落ちる。
//
// ★ isSuper は常に GET /api/admin/tenants のレスポンス由来にする（テスト側で
//   作って渡すと、既定の経路——上限を編集させない——が無検証になる）。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
// 読み仮名辞書は import しただけで取得しに行く（モジュール初期化時）。ここでは
// 管理の面だけを見たいので差し替える。
vi.mock("../../chat/ttsDict.ts", () => ({ setTenantDict: () => {} }));

import { AdminTab } from "./AdminTab.tsx";

const TENANTS = [
  { slug: "acme", name: "Acme", users: 3, running: 1, max_workspaces: 5, max_sessions: 10 },
  { slug: "beta", name: "Beta", users: 1, running: 0 },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function respond(superAdmin: boolean) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/tenants") {
      return Promise.resolve({ tenants: TENANTS, super_admin: superAdmin });
    }
    if (path === "api/admin/ec2-pool") return Promise.resolve({ runtime: "other" });
    if (path === "api/cost/profile") return Promise.resolve({ runtime: "", available: false, verified: false });
    if (path.endsWith("/members")) return Promise.resolve({ members: [] });
    if (path.endsWith("/idp")) return Promise.resolve({ providers: [] });
    return Promise.resolve({});
  });
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<AdminTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rail = () => Array.from(host!.querySelectorAll(".settings-rail-item")).map((b) => b.textContent);
const byText = (sel: string, text: string) =>
  Array.from(host!.querySelectorAll(sel)).find((e) => e.textContent?.includes(text)) as HTMLElement | undefined;
const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

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

describe("AdminTab のレール", () => {
  it("ルートは 一覧 / デプロイ全体 / 横断 の 3 グループで、モードタブは無い", async () => {
    respond(true);
    await mount();
    expect(host!.querySelector(".admin-modes")).toBeNull();
    expect(host!.querySelectorAll(".settings-rail-group").length).toBe(3);
    const items = rail();
    expect(items).toContain("テナント一覧");
    expect(items).toContain("通信");
    expect(items).toContain("セッション");
    // ランタイムが申告しない面は項目ごと出さない（スロット / クラウド費用）。
    expect(items).not.toContain("スロット");
    expect(items).not.toContain("クラウド費用");
  });

  it("テナントを開くとレールがそのテナントの節に入れ替わり、出口から一覧へ戻る", async () => {
    respond(true);
    await mount();
    expect(host!.querySelectorAll(".tenant-card").length).toBe(2);
    await click(byText(".tenant-card", "Acme"));

    // レールはテナントスコープへ（テナント名の見出し＋出口が出る）
    expect(host!.querySelector(".admin-scope-name")?.textContent).toContain("Acme");
    const items = rail();
    expect(items).toContain("上限・自動停止");
    expect(items).toContain("メンバー");
    expect(items).not.toContain("テナント一覧");
    // super_admin なので上限は編集できる（保存ボタンがある）
    expect(byText(".admin-panel h4", "上限")).toBeTruthy();

    await click(host!.querySelector(".admin-rail-back") as HTMLElement);
    expect(rail()).toContain("テナント一覧");
    expect(host!.querySelector(".admin-scope-name")).toBeNull();
  });

  it("super_admin でなければ上限は読み取り専用（テナントの数字だけ）", async () => {
    respond(false);
    await mount();
    await click(byText(".tenant-card", "Acme"));
    expect(host!.querySelector(".tenant-summary")).toBeTruthy();
    expect(host!.querySelector(".admin-actions")).toBeNull();
  });
});
