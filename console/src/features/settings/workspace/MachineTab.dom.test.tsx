// The machine section. What matters is that the two halves of the answer stay two answers:
//   1. on the EC2 slot pool every row is drawn, from the measurement, with the class named
//   2. with no SMBIOS reading the instance type still appears — from the configuration —
//      and says so, rather than the row vanishing
//   3. when the running box is not the configured one, BOTH are on screen
//   4. on a shared host the box's own RAM is never presented as the member's
//   5. stopped: it says these are next-start values instead of showing nothing
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MachineView } from "./MachineTab.tsx";
import type { WsMachine } from "../../../lib/machine.ts";

vi.mock("../../../core/api/client.ts", () => ({
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  getTenant: () => "default",
}));

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
    // The limit leads and the box follows: a member can only spend the former.
    expect(text).toContain("6.50 GiB");
    expect(text).toContain("7.61 GiB");
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
      // The CP strips mem_total and instance_type off a shared host; nothing declares a box.
      measured: { arch: "x86_64", vcpu: 8, mem_max: 10737418240 },
    });
    expect(text).toContain("x86_64");
    expect(text).toContain("10.0 GiB");
    expect(text).toContain("ホストは他の利用者と共有です");
    expect(text).not.toContain("箱の搭載");
  });

  it("shows the next start's box while stopped, and says that is what it is", async () => {
    const text = await mount({ runtime: "ecs-ec2", running: false, declared: ec2.declared });
    expect(text).toContain("停止中です");
    expect(text).toContain("m8g.large");
    expect(text).toContain("設定上");
    expect(text).not.toContain("実測");
  });
});
