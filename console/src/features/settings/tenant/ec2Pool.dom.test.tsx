// The EC2 slot pool surface (docs/log/64 §64.18.6 / ADR 0045 decision 13).
//
// Only the two ways an operator silently loses out are pinned down here:
//   1. The cap has been reached. Nothing grows past it, and the next person to start takes
//      someone else's slot. A row of numbers does not convey that, so it is spelled out.
//   2. The golden snapshot is stale. Forgetting to re-bake is an invisible failure where only
//      new users start on an old CLI, and there is nowhere but this screen to notice it.
// It also checks that a hibernated home reads as "turned into a snapshot", not "gone".
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
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

describe("the EC2 slot pool surface", () => {
  it("counts held, running and hibernating slots and says eviction happens once the cap is reached", async () => {
    await mount();
    expect(text()).toContain("i-hot");
    expect(text()).toContain("i-zzz");
    // A stopped slot is shown as hibernating rather than "stopped": that is by design, not a
    // fault.
    expect(text()).toContain("休止中");
    // slots 2 / max 2, so the next person causes an eviction.
    expect(text()).toContain("立ち退き");
  });

  it("shows no eviction warning while there is room under the cap", async () => {
    api.mockResolvedValue({ ...POOL, max_slots: 8 });
    await mount();
    expect(text()).not.toContain("立ち退き");
  });

  // --- The slot termination stage (docs/log/64 §64.32) ---
  //
  // Stopping halts only the compute; the root volume keeps being billed until the box itself
  // is gone. Not being configured to terminate is a steady state rather than an event, so
  // there is nowhere but this screen to notice it — for the same reason as "there is no golden
  // and nobody has baked one", it is written precisely when the setting is off.
  it("says the root volumes stay to the cap when termination is off", async () => {
    await mount(); // POOL has no slot_terminate_sec = off by default
    expect(text()).toContain("終了しない");
    expect(text()).toContain("2"); // max_slots: without it you cannot tell how many you pay for
  });

  it("says the threshold and the wait the next person pays when termination is on", async () => {
    api.mockResolvedValue({ ...POOL, slot_terminate_sec: 4 * 3600 });
    await mount();
    expect(text()).toContain("4.0 時間"); // matches this screen's existing granularity (fmtIdle)
    expect(text()).not.toContain("終了しない");
    // Without saying what gets faster or slower, an operator cannot decide on a value.
    expect(text()).toContain("135");
  });

  // --- Reconciling the sum of tenant caps against the pool cap (docs/log/64 §64.35) ---
  //
  // The denominator is the easiest thing to get wrong here. A tenant cap counts concurrently
  // running Workspaces; the pool cap counts existing boxes, and a stopped Workspace holds a box
  // while counting against no tenant's quota. Letting this read as "no eviction happens while
  // the sum fits" would make the screen lie.
  it("shows the sum, the capacity and its breakdown when over", async () => {
    api.mockResolvedValue({
      ...POOL,
      max_slots: 30,
      budget: { max_slots: 30, reserved_slots: 2, capacity: 28, allocated: 54, over: true },
    });
    await mount();
    expect(text()).toContain("54");
    expect(text()).toContain("28");
    // Without the "30 − 2" breakdown there is no way to see where 28 came from.
    expect(text()).toContain("30");
    // Reaching the cap means taking or failing, not waiting.
    expect(text()).toContain("立ち退き");
  });

  it("reports the presence of uncapped tenants in different words from being over", async () => {
    api.mockResolvedValue({
      ...POOL,
      budget: { max_slots: 30, reserved_slots: 2, capacity: 28, allocated: 5, over: false, unbounded_tenants: ["acme"] },
    });
    await mount();
    expect(text()).toContain("acme");
    expect(text()).toContain("無制限");
    // Not over, so the over-capacity sentence is not shown.
    expect(text()).not.toContain("超えています");
  });

  it("says the denominators differ in the same place as the numbers", async () => {
    api.mockResolvedValue({
      ...POOL,
      budget: { max_slots: 30, reserved_slots: 2, capacity: 28, allocated: 54, over: true },
    });
    await mount();
    expect(text()).toContain("必要条件であって十分条件ではありません");
  });

  it("shows nothing while it fits, because the server returns no budget then", async () => {
    await mount(); // POOL carries no budget
    expect(text()).not.toContain("必要条件");
  });

  it("shows a hibernated home as a snapshot rather than as gone", async () => {
    await mount();
    expect(text()).toContain("af-ws-acme-carol");
    expect(text()).toContain("退避済み");
  });

  it("says what is happening now when the golden is stale (a mismatch alone is not enough)", async () => {
    api.mockResolvedValue({
      ...POOL,
      golden_image: "ecr/af-workspace:0.7.0",
      golden_stale: true,
    });
    await mount();
    expect(text()).toContain("ecr/af-workspace:0.7.0");
    expect(text()).toContain("空から作られます");
  });

  it("shows the bake procedure when there is no golden (the only place the slow first start is explained)", async () => {
    api.mockResolvedValue({ ...POOL, golden_id: "", golden_image: "", golden_stale: false });
    await mount();
    expect(text()).toContain("bake-golden.sh");
  });

  // --- Bake progress (docs/log/64 §64.30) ---
  //
  // A bake takes around 11 minutes, and there is no snapshot yet during the first half (seed
  // launch, boot-install, releasing the slot). Reporting "there is no golden" throughout that
  // window tells an operator who came to find out why the first start is slow the exact
  // opposite of what is happening.
  const baking = (g: Record<string, unknown>) => ({
    ...POOL,
    golden_id: "",
    golden_image: "",
    auto_bake: true,
    goldens: [{ arch: "x86_64", ...g }],
  });
  const now = () => new Date(Date.now() - 4 * 60 * 1000).toISOString();
  const current = () => host?.querySelector(".bake-step.now")?.textContent || "";

  it("shows which stage the bake is currently in", async () => {
    api.mockResolvedValue(
      baking({
        phase: "boot",
        phase_since: now(),
        seed: { workspace: "af-ws-af-golden-af-golden-seed", instance_id: "i-seed", volume_id: "vol-seed" },
      }),
    );
    await mount();
    expect(current()).toBe("boot-install");
    // Without the elapsed time there is no way to tell progress from a stall.
    expect(text()).toContain("4 分");
    // The seed holds one slot; tie which box is occupied to what it is occupied for.
    expect(text()).toContain("af-ws-af-golden-af-golden-seed");
    expect(text()).toContain("i-seed");
  });

  it("shows the candidate and the progress in the snapshot stage (pending alone does not say whether to wait)", async () => {
    api.mockResolvedValue(baking({ phase: "snapshot", phase_since: now(), candidate: "snap-cand", progress: 63 }));
    await mount();
    expect(current()).toBe("snapshot");
    expect(text()).toContain("snap-cand");
    expect(text()).toContain("63%");
  });

  it("drops the progress line once published and names what is in use", async () => {
    api.mockResolvedValue({
      ...POOL,
      auto_bake: true,
      goldens: [{ arch: "x86_64", phase: "published", snapshot_id: "snap-golden", image: POOL.running_image }],
    });
    await mount();
    expect(host?.querySelector(".bake-steps")).toBeNull();
    expect(text()).toContain("snap-golden");
  });

  // This is what stopped the bake on a real deployment: the guard worked correctly, but the
  // fact that it had fired appeared only in a single line of the CP log.
  it("gives the reason and the numbers when a bake cannot run for lack of slots", async () => {
    api.mockResolvedValue({ ...baking({ phase: "blocked", slots_in_use: 3 }), max_slots: 4 });
    await mount();
    expect(host?.querySelector(".bake-steps")).toBeNull();
    expect(text()).toContain("3/4 使用中");
    expect(text()).toContain("2 つ空き");
  });

  it("says giving up after two failures and auto-bake being off in separate sentences", async () => {
    api.mockResolvedValue(baking({ phase: "gave_up", rejected: "snap-bad", reason: "did not come up", attempts: 2 }));
    await mount();
    expect(text()).toContain("打ち切りました");

    api.mockResolvedValue({ ...baking({ phase: "off" }), auto_bake: false });
    await act(async () => root?.unmount());
    await mount();
    expect(text()).toContain("AF_ECS_EC2_GOLDEN_AUTOBAKE=0");
  });

  it("marks the reserved workspace as being for the bake in the slot table too", async () => {
    api.mockResolvedValue({
      ...baking({ phase: "boot", seed: { workspace: "af-ws-acme-alice", instance_id: "i-hot" } }),
    });
    await mount();
    const occupant = Array.from(host!.querySelectorAll("td")).find((td) => td.textContent?.includes("af-ws-acme-alice"));
    expect(occupant?.textContent).toContain("焼き込み用");
  });

  it("shows no empty table on other runtimes (it would read as the slots having vanished)", async () => {
    api.mockResolvedValue({ runtime: "other" });
    await mount();
    expect(text()).toContain("使っていません");
    expect(host?.querySelector("table")).toBeNull();
  });
});
