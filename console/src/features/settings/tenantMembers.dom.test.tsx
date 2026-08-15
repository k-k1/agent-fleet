// ワークスペースの大きさ（メモリ / CPU / 作業ディスク）を設定する面を固定する
// （docs/63 §63.5 / ADR 0044 決定 1・2）。押さえるのは 2 点だけ:
//   ① 保存で 3 軸すべてが飛ぶこと。この API はクォータ行を丸ごと書くので、UI が
//      送らなかった軸は 0 に落ちる —— disk_gb を省いた実装は、MCP や API で設定した
//      ディスクを黙って消す。
//   ② 名前付きサイズ（S/M/L…）は保存形式ではなく 3 つの入力を埋める近道であること。
//      押した結果が数値として入力欄に入っていなければ、段位が別の状態を持ってしまう。
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

import { MemberView } from "./tenantMembers.tsx";

const MEMBER = {
  user_key: "a-x-com",
  email: "a@x.com",
  role: "member",
  max_sessions: 2,
  mem_limit: 4 * 1024 * 1024 * 1024,
  cpu_limit: 1024,
  disk_gb: 40,
  status: "active",
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <MemberView
        slug="acme"
        member={MEMBER}
        isSuper={false}
        onChanged={() => {}}
        onRemoved={() => {}}
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

const openEditor = async () => {
  const open = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find((b) =>
    (b.textContent || "").includes("上限を設定"),
  );
  await act(async () => open!.click());
};

const numbers = () =>
  Array.from(document.querySelectorAll<HTMLInputElement>(".limit-edit input[type=number]")).map(
    (i) => i.value,
  );

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue({ running: false, sessions: [] });
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("メンバーの上限編集", () => {
  it("保存すると 3 軸すべてを送る（省いた軸は 0 に落ちるため）", async () => {
    await mount();
    await openEditor();
    // 最大セッション / メモリ(MB) / CPU(units) / ディスク(GB) の順で現在値が入る。
    expect(numbers()).toEqual(["2", "4096", "1024", "40"]);

    const save = buttonWith("保存");
    await act(async () => save!.click());

    const [path, method, body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect([path, method]).toEqual(["api/admin/user-limits", "PUT"]);
    expect(body).toMatchObject({
      user_key: "a-x-com",
      tenant_slug: "acme",
      max_sessions: 2,
      mem_limit: 4 * 1024 * 1024 * 1024,
      cpu_limit: 1024,
      disk_gb: 40,
    });
  });

  it("名前付きサイズは 3 つの入力を埋めるだけで、別の状態を持たない", async () => {
    await mount();
    await openEditor();

    const xl = buttonWith("XL");
    await act(async () => xl!.click());
    // XL = 4 vCPU / 16 GiB / 80 GB。Fargate が実際に受け付ける組み合わせであること。
    expect(numbers()).toEqual(["2", "16384", "4096", "80"]);

    const save = buttonWith("保存");
    await act(async () => save!.click());
    const [, , body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect(body).toMatchObject({ mem_limit: 16384 * 1048576, cpu_limit: 4096, disk_gb: 80 });
  });
});
