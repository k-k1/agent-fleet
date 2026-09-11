// The machine section. What matters is that the two halves of the answer stay two answers:
//   1. on the EC2 slot pool every row is drawn, from the measurement, with the class named
//   2. with no SMBIOS reading the instance type still appears — from the configuration —
//      and says so, rather than the row vanishing
//   3. when the running box is not the configured one, BOTH are on screen
//   4. on a shared host the box's own RAM is never presented as the member's
//   5. stopped: it says these are next-start values instead of showing nothing
//   6. the usage charts read their ceilings from the machine above (memory limit, core
//      count), which is the reason they are on this tab and not only in the WS bar
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MachineView } from "./MachineTab.tsx";
import type { WsMachine } from "../../../lib/machine.ts";
import { __test as feed } from "../../../core/store/wsStatsFeed.ts";

vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  getTenant: () => "default",
}));
// The feed is driven directly (feed.sample()) rather than by its 4s timer.
vi.mock("../../../core/push/events.ts", () => ({ onPush: () => () => {}, pushHealthy: () => true }));

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(d: WsMachine) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MachineView d={d} />);
  });
  return host.textContent || "";
}

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  feed.reset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const ec2: WsMachine = {
  runtime: "ecs-ec2",
  running: true,
  measured: {
    arch: "arm64",
    vcpu: 2,
    mem_max: 6979321856, // 6656 MiB
    mem_total: 8174716928,
    disk_total: 64424509440,
    instance_type: "m8g.large",
  },
  declared: {
    instance_type: "m8g.large",
    arch: "arm64",
    vcpu: 2,
    slot_mem_mib: 8192,
    mem_cap_mib: 6656,
    home_gib: 60,
    class_id: "arm",
    class_label: "低コスト (Arm)",
    dedicated: true,
  },
};

describe("MachineView", () => {
  it("shows the measured box, its class and both memory figures", async () => {
    const text = await mount(ec2);
    expect(text).toContain("m8g.large");
    expect(text).toContain("arm64");
    expect(text).toContain("低コスト (Arm)");
    // The limit leads and the instance follows: a member can only spend the former.
    expect(text).toContain("6.50 GiB");
    expect(text).toContain("7.61 GiB");
    // 🔴 The label itself, so the "shared host" test below is a live check. Without a POSITIVE
    // assertion here, its `not.toContain` passes for the wrong reason the moment this string
    // is reworded, and "the shared host's RAM is not presented as the member's own" stops
    // being tested at all.
    expect(text).toContain("インスタンスの搭載");
    expect(text).toContain("実測");
    expect(text).not.toContain("設定上");
  });

  it("falls back to the configured box when nothing could be measured, and says so", async () => {
    const text = await mount({
      ...ec2,
      measured: { arch: "arm64", vcpu: 2 }, // no SMBIOS, no cgroup reading
    });
    expect(text).toContain("m8g.large"); // still named — from the configuration
    expect(text).toContain("設定上");
  });

  it("keeps a disagreement between the running and the configured box visible", async () => {
    const text = await mount({
      ...ec2,
      declared: { ...ec2.declared, instance_type: "m8g.xlarge", slot_mem_mib: 16384, mem_cap_mib: 14336 },
    });
    expect(text).toContain("m8g.large"); // what it is on now
    expect(text).toContain("m8g.xlarge"); // what the next start will use
  });

  // Without an instance type to compare, the memory limit answers the same question: a
  // different rung always means a different cap.
  it("detects the same disagreement from the memory limit alone", async () => {
    const text = await mount({
      ...ec2,
      measured: { arch: "arm64", vcpu: 2, mem_max: 6979321856 },
      declared: { ...ec2.declared, instance_type: "", mem_cap_mib: 14336 },
    });
    expect(text).toContain("次に起動すると割り当てが変わります");
  });

  it("never presents a shared host's RAM as the member's own machine", async () => {
    const text = await mount({
      runtime: "docker",
      running: true,
      // The CP strips mem_total and instance_type off a shared host; nothing declares an instance.
      measured: { arch: "x86_64", vcpu: 8, mem_max: 10737418240 },
    });
    expect(text).toContain("x86_64");
    expect(text).toContain("10.0 GiB");
    expect(text).toContain("ホストは他の利用者と共有です");
    expect(text).not.toContain("インスタンスの搭載");
  });

  it("shows the next start's box while stopped, and says that is what it is", async () => {
    const text = await mount({ runtime: "ecs-ec2", running: false, declared: ec2.declared });
    expect(text).toContain("停止中です");
    expect(text).toContain("m8g.large");
    expect(text).toContain("設定上");
    expect(text).not.toContain("実測");
  });

  it("draws the usage charts against the machine's own ceilings", async () => {
    // 4 GiB of an 8 GiB rung capped at 6.5 GiB, and 150% of a 2-vCPU box.
    feed.setLatest({ running: true, mem_used: 4294967296, mem_max: 6979321856, cpu_pct: 150 });
    feed.sample();
    feed.sample();
    const text = await mount(ec2);
    expect(text).toContain("使用状況");
    // The memory line is stated against the LIMIT, not against the box's RAM.
    expect(text).toContain("4.00 GiB / 6.50 GiB");
    // CPU's ceiling is the core count, so 150% of 2 vCPU is legible as such.
    expect(text).toContain("150% / 200%");
    expect(host!.querySelectorAll("svg.trend").length).toBe(2);
  });

  it("says an OOM kill happened, even after the flag has gone", async () => {
    feed.setLatest({ running: true, mem_used: 4294967296, mem_max: 6979321856, cpu_pct: 5, oom_recent: true });
    feed.sample();
    feed.setLatest({ running: true, mem_used: 4294967296, mem_max: 6979321856, cpu_pct: 5 });
    feed.sample();
    const text = await mount(ec2);
    expect(text).toContain("メモリ不足でプロセスが強制終了");
  });

  it("shows no usage block for a stopped workspace", async () => {
    feed.setLatest({ running: true, mem_used: 4294967296, mem_max: 6979321856, cpu_pct: 5 });
    feed.sample();
    const text = await mount({ runtime: "ecs-ec2", running: false, declared: ec2.declared });
    expect(text).not.toContain("使用状況");
  });
});
