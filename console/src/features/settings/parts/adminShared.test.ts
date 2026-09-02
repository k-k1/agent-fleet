// adminShared の純粋な表示ロジック。DOM は要らないので node プロジェクト側。
import { describe, it, expect } from "vitest";
import { slotMemLabel } from "./adminShared.ts";

describe("slotMemLabel", () => {
  // Two numbers exist once the workspace is capped, and the one a member can actually
  // spend is the cgroup's. Printing only the box would promise memory the kernel will
  // not hand over (ADR 0045 決定 28).
  const tr = ((k: string, v?: Record<string, string>) => `${v?.n} of ${v?.box}`) as never;

  it("leads with what the workspace gets and follows with the box", () => {
    expect(slotMemLabel(tr, { instance_type: "m7i.large", mem_mib: 8192, usable_mem_mib: 6554 })).toBe("6.4 GiB of 8 GiB");
  });

  it("prints one number while the deployment runs uncapped", () => {
    expect(slotMemLabel(tr, { instance_type: "m7i.large", mem_mib: 8192 })).toBe("8 GiB");
  });

  // A cap equal to the rung is not a cap; saying "8 GiB of 8 GiB" would be noise.
  it("prints one number when the cap equals the box", () => {
    expect(slotMemLabel(tr, { instance_type: "m7i.large", mem_mib: 8192, usable_mem_mib: 8192 })).toBe("8 GiB");
  });
});
