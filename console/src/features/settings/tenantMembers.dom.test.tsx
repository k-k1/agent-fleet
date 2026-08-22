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

async function mount(member: typeof MEMBER & { state?: string } = MEMBER, onRemoved = () => {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <MemberView
        slug="acme"
        member={member}
        isSuper={false}
        onChanged={() => {}}
        onRemoved={onRemoved}
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

// 後始末の 3 段（docs/61 §61.18）。段は「外す → Workspace を破棄 → 行を消す」で、
// 画面に出る危険操作は常にそのうちの 1 つだけであること —— 3 つ並べると、どれが
// 今できる操作なのかが押してみるまで分からない。
describe("外したメンバーの後始末", () => {
  it("在席中は「メンバーを外す」だけ", async () => {
    await mount();
    expect(buttonWith("メンバーを外す")).toBeTruthy();
    expect(buttonWith("Workspace を破棄")).toBeFalsy();
    expect(buttonWith("メンバーを完全に削除")).toBeFalsy();
  });

  it("外した直後・Workspace が残っている間は「破棄」だけ", async () => {
    await mount({ ...MEMBER, status: "removed", state: "stopped" });
    expect(buttonWith("Workspace を破棄")).toBeTruthy();
    // まだ home もクラウド資源も生きている。行を消すとそれを指すものが無くなる。
    expect(buttonWith("メンバーを完全に削除")).toBeFalsy();
  });

  it("破棄が済んだ（state=none）ときだけ「完全に削除」が出て、DELETE を投げる", async () => {
    const onRemoved = vi.fn();
    await mount({ ...MEMBER, status: "removed", state: "none" }, onRemoved);
    expect(buttonWith("Workspace を破棄")).toBeFalsy();

    await act(async () => buttonWith("メンバーを完全に削除")!.click());
    const confirm = buttonWith("完全に削除する");
    expect(confirm).toBeTruthy();
    await act(async () => confirm!.click());

    const call = apiJSON.mock.calls.find((c) => String(c[0]).includes("/members/"))!;
    expect(call[0]).toBe("api/admin/tenants/acme/members/a-x-com");
    expect(call[1]).toBe("DELETE");
    expect(onRemoved).toHaveBeenCalled();
  });
});

// EC2 スロットプール（ADR 0045 決定 21）。ここで押さえるのは「効かない入力欄を出さない」
// ことと、「隠した副作用で保存済みの値を 0 に落とさない」ことの 2 点である —— 前者だけ
// 直すと、CPU 欄を隠した瞬間に他ランタイム用に設定された cpu_limit が消える。
const SIZING_EC2 = {
  runtime: "ecs-ec2",
  cpu_effective: false,
  mem_meaning: "slot",
  disk_meaning: "home",
  disk_default_gb: 50,
  disk_create_only: true,
  slots: [
    { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
    { instance_type: "m7i.xlarge", mem_mib: 16384, vcpu: 4 },
    { instance_type: "m7i.2xlarge", mem_mib: 32768, vcpu: 8 },
  ],
};

describe("メンバーの上限編集（ecs-ec2）", () => {
  beforeEach(() => {
    api.mockImplementation((p: string) =>
      p === "api/admin/workspace-sizing"
        ? Promise.resolve(SIZING_EC2)
        : Promise.resolve({ running: false, sessions: [] }),
    );
  });

  it("CPU 欄を出さない。ただし保存では保存済みの cpu_limit をそのまま送り返す", async () => {
    await mount();
    await openEditor();
    // 最大セッション / メモリ(MB) / ディスク(GB)。CPU 欄が消えている。
    expect(numbers()).toEqual(["2", "4096", "40"]);

    const save = buttonWith("保存");
    await act(async () => save!.click());
    const [, , body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect(body).toMatchObject({ mem_limit: 4096 * 1048576, cpu_limit: 1024, disk_gb: 40 });
  });

  it("プリセットは梯子そのもので、メモリ軸だけを動かす", async () => {
    await mount();
    await openEditor();
    const chips = Array.from(document.querySelectorAll<HTMLButtonElement>(".le-presets .chip")).map(
      (b) => (b.textContent || "").trim(),
    );
    expect(chips).toEqual(["8 GiB", "16 GiB", "32 GiB"]);

    await act(async () => buttonWith("16 GiB")!.click());
    expect(numbers()).toEqual(["2", "16384", "40"]);
  });

  it("メモリ欄は「上限」ではなく、実際に乗る箱を言う", async () => {
    await mount();
    await openEditor();
    const units = Array.from(document.querySelectorAll(".limit-edit .af-unit")).map((e) =>
      (e.textContent || "").trim(),
    );
    // 4096 MB は m7i.large に乗る（そして 8 GiB 丸ごと使える）。
    expect(units).toContain("→ m7i.large（2 vCPU / 8 GiB・専有）");
    // ディスクは作業ディスクではなく永続 home である、と言う。
    expect(units.some((u) => u.includes("home の作成時にだけ反映され"))).toBe(true);
    expect(units.some((u) => u.includes("作業ディスクは停止すると消えます"))).toBe(false);
  });
});
