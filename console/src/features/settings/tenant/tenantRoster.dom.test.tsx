// The roster row states what each member is running on (docs/log/70 §70.14.7).
//
// A slot runtime shows the instance, everything else shows the numbers, because they are
// different claims: on ecs-ec2 the memory figure is not a cap but a request that selects the
// instance the member then gets in full, so "8192 MB" says only half of it and
// "m6i.large · 2 vCPU / 8 GiB" is the answer.
// CPU follows the same rule as the member detail: with cpu_effective=false it is not shown, as
// printing a number that has no effect is worse than omitting the field.
// "Unset" is handled per axis. The numeric axes (disk, sessions) stay silent, because a column
// of zeros stops being read. The instance does not stay silent: memory 0 on ecs-ec2 means
// landing on the smallest step, not "the default", so hiding it would be the lie.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

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

describe("member roster shows the size actually in effect", () => {
  it("shows the instance on a slot runtime, and a different one per class", async () => {
    await mountRoster(SIZING_CLASSES);
    // 8 GiB in the arm class is an m7g.large.
    expect(rowText(0)).toContain("m7g.large");
    expect(rowText(0)).toContain("2 vCPU / 8 GiB");
    expect(rowText(0)).toContain("s≤3");
    // No class means the default (standard), where 4 GiB lands on an m7i.large.
    expect(rowText(1)).toContain("m7i.large");
  });

  it("does not print a CPU number when cpu_effective=false", async () => {
    await mountRoster(SIZING_CLASSES);
    // The only vCPU allowed on the row is the instance's spec; a bare "2 vCPU" derived from
    // cpu_limit=2048 must not appear.
    const sizes = Array.from(document.querySelectorAll(".member-row .mr-size")).map((e) => e.textContent || "");
    expect(sizes.some((t) => t === "2 vCPU")).toBe(false);
    expect(sizes.some((t) => t.includes("2 vCPU / 8 GiB"))).toBe(true);
  });

  // On a slot runtime the instance is shown even when unset: memory 0 on ecs-ec2 is not "the
  // deployment default" but landing on the smallest step (slotTypeFor(0)), so hiding it would
  // be the lie — the member detail says the same via `admin.ws_slot_zero`. The "stay silent
  // when unset" rule applies only to the numeric axes (disk, sessions).
  it("shows the instance even when unset, but prints no zeros on the numeric axes", async () => {
    await mountRoster(SIZING_CLASSES);
    expect(rowText(2)).toContain("m7i.large"); // it does land on the smallest step
    expect(rowText(2)).not.toContain("s≤");
    expect(rowText(2)).not.toContain("ディスク");
  });

  it("prints no numbers on an unset row when the runtime has no slots", async () => {
    await mountRoster({ runtime: "local", cpu_effective: true, mem_meaning: "limit", disk_meaning: "quota" });
    expect(rowText(2)).not.toContain("GiB");
    expect(rowText(2)).not.toContain("vCPU");
    expect(rowText(2)).not.toContain("s≤");
  });

  it("shows the numbers rather than an instance when the runtime has no slots", async () => {
    await mountRoster({ runtime: "local", cpu_effective: true, mem_meaning: "limit", disk_meaning: "quota" });
    expect(rowText(0)).not.toContain("m7g");
    expect(rowText(0)).toContain("8 GiB");
    expect(rowText(0)).toContain("2 vCPU");
  });
});
