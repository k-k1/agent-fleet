// テナント上限の保存と、スロットプールとの突き合わせ（docs/log/64 §64.35 / ADR 0045 決定 25）。
//
// この画面の検証は端末履歴の保持日数と時間文字列の書式しか見ておらず、**プール上限を
// 超える配分が黙って保存できた**。超過は設定画面から最も遠いところ——「枠内なのに起動
// できない」「他テナントのスロットが立ち退きになる」——でしか表に出ない。
//
// 固定したいのは 3 点:
//   ① 超過は**拒否ではなく警告**であること（保存は通る）。既に超過している配備を
//      この画面ごと凍らせないため。
//   ② 打ち間違い（負の数）は**拒否**であること。0 は無制限なので、負は「小さい上限」
//      ではなく誰も満たせない上限になる。
//   ③ 収まっているときは何も出さないこと。毎回「大丈夫です」が出ると読まれなくなる。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
const toast = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));

import { TenantLimits } from "./tenantScope.tsx";

const TENANT = { slug: "acme", name: "Acme", max_workspaces: 4 } as never;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantLimits slug="acme" tenant={TENANT} hasPool onChanged={() => {}} />);
  });
}

const text = () => host?.textContent || "";

async function save() {
  const btn = [...(host?.querySelectorAll("button") || [])].find((b) => b.className.includes("primary"));
  await act(async () => {
    btn?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("テナント上限とスロットプールの突き合わせ", () => {
  it("超過は保存したうえで警告する（拒否しない）", async () => {
    apiJSON.mockResolvedValue({
      tenant: "acme",
      max_workspaces: 50,
      pool_budget: { max_slots: 10, reserved_slots: 2, capacity: 8, allocated: 54, over: true },
    });
    await mount();
    await save();
    // 保存済みの表示は出る——「保存できなかった」と読ませない。
    expect(text()).toContain("54");
    expect(text()).toContain("8");
    // エラートーストではない。
    expect(toast).not.toHaveBeenCalled();
  });

  it("★分母の違いを、警告と同じ場所で言う", async () => {
    apiJSON.mockResolvedValue({
      pool_budget: { max_slots: 10, reserved_slots: 2, capacity: 8, allocated: 54, over: true },
    });
    await mount();
    await save();
    // 「同時に動いている WS」と「存在している箱」を混ぜて読ませない。停止中の WS は
    // どのテナント枠にも数えられないまま箱を掴んでいる。
    expect(text()).toContain("必要条件であって十分条件ではありません");
  });

  it("収まっていればサーバが何も返さないので、何も出ない", async () => {
    apiJSON.mockResolvedValue({ tenant: "acme", max_workspaces: 6 });
    await mount();
    await save();
    expect(text()).not.toContain("必要条件");
    expect(toast).not.toHaveBeenCalled();
  });

  it("サーバが拒否したときは従来どおりトーストで、警告欄は出さない", async () => {
    apiJSON.mockResolvedValue({ error: { message: "max_workspaces cannot be negative (0 = unlimited)" } });
    await mount();
    await save();
    expect(toast).toHaveBeenCalledWith("max_workspaces cannot be negative (0 = unlimited)");
    expect(text()).not.toContain("必要条件");
  });

  it("同時利用の上限であることを欄の隣に書く（占有スロット数ではない）", async () => {
    await mount();
    expect(text()).toContain("同時に動く数");
  });
});
