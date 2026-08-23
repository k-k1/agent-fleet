// EC2 スロットプールの面（docs/64 §64.18.6 / ADR 0045 決定 13）。
//
// 押さえるのは「運用者が黙って損をする 2 つ」だけ:
//   ① 上限に達していること。ここから先は増えず、次に起動する人は**他人のスロットを
//      取り上げる**。数字が並んでいるだけでは伝わらないので、文言として出す。
//   ② golden snapshot が古いこと。焼き直し忘れは、新規ユーザーだけが古い CLI で
//      始まるという見えない失敗で、この画面以外に気づく場所が無い。
// あわせて、退避中の home が「消えた」ではなく「snapshot になった」と読めることも見る。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

import { PoolView } from "./ec2Pool.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<PoolView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host?.textContent || "";

const POOL = {
  runtime: "ecs-ec2",
  pool: "af",
  max_slots: 2,
  slot_sleep_sec: 900,
  hibernate_after_sec: 30 * 24 * 3600,
  running_image: "ecr/af-workspace:0.9.0",
  slots: [
    { instance_id: "i-hot", instance_type: "m7i.large", az: "ap-northeast-1a", state: "running", registered: true, workspace: "af-ws-acme-alice", idle_minutes: 0 },
    { instance_id: "i-zzz", instance_type: "m7i.large", az: "ap-northeast-1a", state: "stopped", registered: false, workspace: "af-ws-acme-bob", idle_minutes: 4320 },
  ],
  homes: [
    { volume_id: "vol-1", workspace: "af-ws-acme-alice", size_gib: 50, az: "ap-northeast-1a", attached_to: "i-hot", idle_minutes: 0, hibernating: false, snapshot_id: "", snapshot_state: "" },
    { volume_id: "", workspace: "af-ws-acme-carol", size_gib: 0, az: "", attached_to: "", idle_minutes: 0, hibernating: true, snapshot_id: "snap-1", snapshot_state: "completed" },
  ],
  golden_id: "snap-golden",
  golden_image: "ecr/af-workspace:0.9.0",
  golden_stale: false,
};

beforeEach(() => {
  api.mockReset();
  api.mockResolvedValue(POOL);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("EC2 スロットプールの面", () => {
  it("確保数・起動中・休止中を数え、上限に達していれば立ち退きが起きると書く", async () => {
    await mount();
    expect(text()).toContain("i-hot");
    expect(text()).toContain("i-zzz");
    // 停止中のスロットは「stopped」ではなく休止中として見せる（異常ではなく設計どおり）。
    expect(text()).toContain("休止中");
    // slots 2 / max 2 なので、次の人は立ち退きになる。
    expect(text()).toContain("立ち退き");
  });

  it("上限に余裕があるときは立ち退きの警告を出さない", async () => {
    api.mockResolvedValue({ ...POOL, max_slots: 8 });
    await mount();
    expect(text()).not.toContain("立ち退き");
  });

  it("退避済みの home は「消えた」ではなく snapshot として見える", async () => {
    await mount();
    expect(text()).toContain("af-ws-acme-carol");
    expect(text()).toContain("退避済み");
  });

  it("golden が古いときは、いま何が起きているかを書く（一致しない、では足りない）", async () => {
    api.mockResolvedValue({
      ...POOL,
      golden_image: "ecr/af-workspace:0.7.0",
      golden_stale: true,
    });
    await mount();
    expect(text()).toContain("ecr/af-workspace:0.7.0");
    expect(text()).toContain("空から作られます");
  });

  it("golden が無ければ、焼く手順を出す（初回起動が遅い理由がここにしか無い）", async () => {
    api.mockResolvedValue({ ...POOL, golden_id: "", golden_image: "", golden_stale: false });
    await mount();
    expect(text()).toContain("bake-golden.sh");
  });

  // --- 焼き込みの進み具合（docs/64 §64.30）---
  //
  // 焼きは 11 分前後かかり、前半（種の起動・boot-install・スロット解放）には snapshot が
  // まだ無い。以前の画面はその間ずっと「golden はありません」と言っていて、初回起動が
  // 遅い理由を見に来た運用者は**起きていることの逆**を読まされていた。
  const baking = (g: Record<string, unknown>) => ({
    ...POOL,
    golden_id: "",
    golden_image: "",
    auto_bake: true,
    goldens: [{ arch: "x86_64", ...g }],
  });
  const now = () => new Date(Date.now() - 4 * 60 * 1000).toISOString();
  const current = () => host?.querySelector(".bake-step.now")?.textContent || "";

  it("焼いている最中は、いまどの段にいるかが読める", async () => {
    api.mockResolvedValue(
      baking({
        phase: "boot",
        phase_since: now(),
        seed: { workspace: "af-ws-af-golden-af-golden-seed", instance_id: "i-seed", volume_id: "vol-seed" },
      }),
    );
    await mount();
    expect(current()).toBe("boot-install");
    // 経過時間が無いと、動いているのか固まっているのかが読めない。
    expect(text()).toContain("4 分");
    // 種はスロットを 1 つ握る。どの箱が何のために埋まっているのかを繋げる。
    expect(text()).toContain("af-ws-af-golden-af-golden-seed");
    expect(text()).toContain("i-seed");
  });

  it("snapshot 段では候補と進捗を出す（pending だけでは待つべきか分からない）", async () => {
    api.mockResolvedValue(baking({ phase: "snapshot", phase_since: now(), candidate: "snap-cand", progress: 63 }));
    await mount();
    expect(current()).toBe("snapshot");
    expect(text()).toContain("snap-cand");
    expect(text()).toContain("63%");
  });

  it("公開済みなら進行線は出さず、使っているものを書く", async () => {
    api.mockResolvedValue({
      ...POOL,
      auto_bake: true,
      goldens: [{ arch: "x86_64", phase: "published", snapshot_id: "snap-golden", image: POOL.running_image }],
    });
    await mount();
    expect(host?.querySelector(".bake-steps")).toBeNull();
    expect(text()).toContain("snap-golden");
  });

  // 実デプロイ（本番配備）で焼きを止めたのはこれ。歯止めは正しく効いていたのに、
  // 効いたことが CP ログの 1 行にしか出ていなかった。
  it("スロット不足で焼けないときは、その理由と数を出す", async () => {
    api.mockResolvedValue({ ...baking({ phase: "blocked", slots_in_use: 3 }), max_slots: 4 });
    await mount();
    expect(host?.querySelector(".bake-steps")).toBeNull();
    expect(text()).toContain("3/4 使用中");
    expect(text()).toContain("2 つ空き");
  });

  it("2 回失敗して打ち切ったことと、自動焼きが切られていることは別の文で言う", async () => {
    api.mockResolvedValue(baking({ phase: "gave_up", rejected: "snap-bad", reason: "did not come up", attempts: 2 }));
    await mount();
    expect(text()).toContain("打ち切りました");

    api.mockResolvedValue({ ...baking({ phase: "off" }), auto_bake: false });
    await act(async () => root?.unmount());
    await mount();
    expect(text()).toContain("AF_ECS_EC2_GOLDEN_AUTOBAKE=0");
  });

  it("予約 workspace はスロット表でも「焼き込み用」と分かる", async () => {
    api.mockResolvedValue({
      ...baking({ phase: "boot", seed: { workspace: "af-ws-acme-alice", instance_id: "i-hot" } }),
    });
    await mount();
    const occupant = Array.from(host!.querySelectorAll("td")).find((td) => td.textContent?.includes("af-ws-acme-alice"));
    expect(occupant?.textContent).toContain("焼き込み用");
  });

  it("他のランタイムでは空の表を出さない（スロットが消えたと読めるため）", async () => {
    api.mockResolvedValue({ runtime: "other" });
    await mount();
    expect(text()).toContain("使っていません");
    expect(host?.querySelector("table")).toBeNull();
  });
});
