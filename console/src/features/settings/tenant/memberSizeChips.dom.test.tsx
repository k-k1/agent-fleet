// The roster draws the box the CP resolved for the next start (ADR 0139), not the stored size's.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemberSizeChips } from "./tenantMembers.tsx";
import type { Member, WsSizing } from "../parts/adminShared.ts";

const sizing: WsSizing = {
  runtime: "ecs-ec2",
  cpu_effective: false,
  mem_meaning: "slot",
  disk_meaning: "home",
  slots: [
    { instance_type: "m7i.large", mem_mib: 8192 },
    { instance_type: "m7i.xlarge", mem_mib: 16384 },
  ],
} as WsSizing;

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
});

async function mount(m: Partial<Member>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MemberSizeChips m={{ user_key: "u", role: "member", ...m } as Member} sizing={sizing} />);
  });
  return host.textContent || "";
}

describe("MemberSizeChips", () => {
  it("draws the stored box when nothing overrides it", async () => {
    const t = await mount({ mem_limit: 0, next_start: { slot_class: "", mem_mib: 8192, source: "stored" } });
    expect(t).toContain("m7i.large");
    expect(t).not.toContain("ブースト中");
  });

  it("draws the boosted box, and says so, though the stored size is 0 (the smallest rung)", async () => {
    const t = await mount({ mem_limit: 0, next_start: { slot_class: "", mem_mib: 16384, source: "boost", until: "2026-11-17T18:00:00Z" } });
    expect(t).toContain("m7i.xlarge");
    expect(t).toContain("ブースト中");
  });
});
