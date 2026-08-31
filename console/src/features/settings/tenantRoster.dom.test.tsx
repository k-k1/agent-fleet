// 名簿の行に「何で動いているか」を出す（docs/log/70 §70.14.7）。
//
// ⚠️ スロット型のランタイムでは「箱」を、それ以外では「数値」を出す —— 別の主張だから
// である。ecs-ec2 のメモリは上限ではなく「載る箱を選ぶ要求」で、本人はその箱を丸ごと
// 使う。だから "8192 MB" は半分しか言っておらず、"m6i.large · 2 vCPU / 8 GiB" が答え。
// ⚠️ CPU はメンバー詳細と同じ規則で、cpu_effective=false のときは出さない（効かない
// 数字を画面に出すのは、無い欄より悪い）。
// ⚠️ 未設定の扱いは軸で違う。数値の軸（ディスク・セッション）は黙る——全行が 0 で埋まった
// 列は読まれなくなる。**箱は黙らない**: ecs-ec2 のメモリ 0 は「既定」ではなく最小の段に
// 載ることなので、伏せる方が嘘になる。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MembersPanel } from "./tenantMembers.tsx";

const SIZING_CLASSES = {
  runtime: "ecs-ec2",
  cpu_effective: false,
  mem_meaning: "slot",
  disk_meaning: "home",
  disk_default_gb: 50,
  default_slot_class: "standard",
  slots: [
    { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
    { instance_type: "m7i.xlarge", mem_mib: 16384, vcpu: 4 },
  ],
  slot_classes: [
    {
      id: "standard",
      label: "標準（Intel）",
      arch: "x86_64",
      slots: [
        { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
        { instance_type: "m7i.xlarge", mem_mib: 16384, vcpu: 4 },
      ],
    },
    {
      id: "arm",
      label: "省コスト（Arm）",
      arch: "arm64",
      slots: [
        { instance_type: "m7g.large", mem_mib: 8192, vcpu: 2 },
        { instance_type: "m7g.xlarge", mem_mib: 16384, vcpu: 4 },
      ],
    },
  ],
};

const ROSTER = [
  { user_key: "a", email: "a@x.com", role: "member", max_sessions: 3, mem_limit: 8 * 1073741824, cpu_limit: 2048, slot_class: "arm" },
  { user_key: "b", email: "b@x.com", role: "member", mem_limit: 4 * 1073741824 },
  { user_key: "c", email: "c@x.com", role: "member" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mountRoster(sizing: unknown) {
  api.mockImplementation((p: string) =>
    p === "api/admin/workspace-sizing" ? Promise.resolve(sizing) : Promise.resolve({ members: ROSTER }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MembersPanel slug="acme" isSuper={false} onOpenMember={() => {}} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rowText = (i: number) => (document.querySelectorAll(".member-row")[i]?.textContent || "").replace(/\s+/g, " ");

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("メンバー名簿に稼働している大きさを出す", () => {
  it("スロット型では「乗る箱」を出し、クラスに応じて別の箱になる", async () => {
    await mountRoster(SIZING_CLASSES);
    // arm クラスの 8 GiB は m7g.large。
    expect(rowText(0)).toContain("m7g.large");
    expect(rowText(0)).toContain("2 vCPU / 8 GiB");
    expect(rowText(0)).toContain("s≤3");
    // クラス未指定 = 既定（standard）の 4 GiB は m7i.large に載る。
    expect(rowText(1)).toContain("m7i.large");
  });

  it("cpu_effective=false のとき CPU の数値は出さない", async () => {
    await mountRoster(SIZING_CLASSES);
    // 行に出てよい vCPU は「箱の仕様」だけ。cpu_limit=2048 由来の "2 vCPU" 単独は出ない。
    const sizes = Array.from(document.querySelectorAll(".member-row .mr-size")).map((e) => e.textContent || "");
    expect(sizes.some((t) => t === "2 vCPU")).toBe(false);
    expect(sizes.some((t) => t.includes("2 vCPU / 8 GiB"))).toBe(true);
  });

  // ⚠️ スロット型では「未設定」でも箱は出す。ecs-ec2 のメモリ 0 は「デプロイ既定」では
  // なく **最小の段に載る**（slotTypeFor(0)）ので、箱を伏せる方が嘘になる——メンバー詳細
  // が `admin.ws_slot_zero` で同じことを言っている。「未設定なら黙る」規則が効くのは
  // 数値の軸（ディスク・セッション）の方だけである。
  it("未設定でも箱は出す。ただし数値の軸は 0 を並べない", async () => {
    await mountRoster(SIZING_CLASSES);
    expect(rowText(2)).toContain("m7i.large"); // 最小の段に載る、が事実
    expect(rowText(2)).not.toContain("s≤");
    expect(rowText(2)).not.toContain("ディスク");
  });

  it("スロットの無いランタイムでは、未設定の行に数値を並べない", async () => {
    await mountRoster({ runtime: "local", cpu_effective: true, mem_meaning: "limit", disk_meaning: "quota" });
    expect(rowText(2)).not.toContain("GiB");
    expect(rowText(2)).not.toContain("vCPU");
    expect(rowText(2)).not.toContain("s≤");
  });

  it("スロットが無いランタイムでは箱ではなく数値を出す", async () => {
    await mountRoster({ runtime: "local", cpu_effective: true, mem_meaning: "limit", disk_meaning: "quota" });
    expect(rowText(0)).not.toContain("m7g");
    expect(rowText(0)).toContain("8 GiB");
    expect(rowText(0)).toContain("2 vCPU");
  });
});
