// Pins the view that sets the workspace size (memory / CPU / work disk)
// (docs/log/63 §63.5, ADR 0044 decisions 1 and 2). Two things only:
//   1. A save sends all three axes. The API writes the whole quota row, so any axis the UI
//      omits drops to 0 — an implementation leaving out disk_gb silently erases a disk that
//      was set through MCP or the API.
//   2. The named sizes (S/M/L, ...) are a shortcut that fills the three inputs, not a storage
//      format. If pressing one does not land as numbers in the fields, the size carries state
//      of its own.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MemberView } from "./tenantMemberDetail.tsx";

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

describe("member limit editing", () => {
  it("sends all three axes on save, since an omitted axis drops to 0", async () => {
    await mount();
    await openEditor();
    // Current values in order: max sessions / memory (MB) / CPU (units) / disk (GB).
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

  it("named sizes only fill the three inputs and carry no state of their own", async () => {
    await mount();
    await openEditor();

    const xl = buttonWith("XL");
    await act(async () => xl!.click());
    // XL = 4 vCPU / 16 GiB / 80 GB, a combination Fargate actually accepts.
    expect(numbers()).toEqual(["2", "16384", "4096", "80"]);

    const save = buttonWith("保存");
    await act(async () => save!.click());
    const [, , body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect(body).toMatchObject({ mem_limit: 16384 * 1048576, cpu_limit: 4096, disk_gb: 80 });
  });
});

// Cleanup runs in three stages (docs/log/61 §61.18): remove the member, discard the workspace,
// delete the row. Exactly one of them is on screen at a time; showing all three leaves the
// operator unable to tell which one is possible now without pressing it.
describe("cleanup of a removed member", () => {
  it("offers only remove while the member is still present", async () => {
    await mount();
    expect(buttonWith("メンバーを外す")).toBeTruthy();
    expect(buttonWith("Workspace を破棄")).toBeFalsy();
    expect(buttonWith("メンバーを完全に削除")).toBeFalsy();
  });

  it("offers only discard just after removal, while the workspace still exists", async () => {
    await mount({ ...MEMBER, status: "removed", state: "stopped" });
    expect(buttonWith("Workspace を破棄")).toBeTruthy();
    // Home and the cloud resources are still alive; deleting the row would leave nothing
    // pointing at them.
    expect(buttonWith("メンバーを完全に削除")).toBeFalsy();
  });

  it("offers permanent deletion only once discarded (state=none), and sends DELETE", async () => {
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

// The EC2 slot pool (ADR 0045 decision 21). Two things: no input that has no effect is shown,
// and hiding one must not drop a stored value to 0. Fixing only the first would erase a
// cpu_limit set for another runtime the moment the CPU field is hidden.
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

describe("member limit editing (ecs-ec2)", () => {
  beforeEach(() => {
    api.mockImplementation((p: string) =>
      p === "api/admin/workspace-sizing"
        ? Promise.resolve(SIZING_EC2)
        : Promise.resolve({ running: false, sessions: [] }),
    );
  });

  it("hides the CPU field but still sends the stored cpu_limit back on save", async () => {
    await mount();
    await openEditor();
    // Max sessions / memory (MB) / disk (GB): the CPU field is gone.
    expect(numbers()).toEqual(["2", "4096", "40"]);

    const save = buttonWith("保存");
    await act(async () => save!.click());
    const [, , body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect(body).toMatchObject({ mem_limit: 4096 * 1048576, cpu_limit: 1024, disk_gb: 40 });
  });

  it("presets are the ladder itself and move only the memory axis", async () => {
    await mount();
    await openEditor();
    const chips = Array.from(document.querySelectorAll<HTMLButtonElement>(".le-presets .chip")).map(
      (b) => (b.textContent || "").trim(),
    );
    expect(chips).toEqual(["8 GiB", "16 GiB", "32 GiB"]);

    await act(async () => buttonWith("16 GiB")!.click());
    expect(numbers()).toEqual(["2", "16384", "40"]);
  });

  it("states the instance actually landed on, not a cap, in the memory field", async () => {
    await mount();
    await openEditor();
    const units = Array.from(document.querySelectorAll(".limit-edit .af-unit")).map((e) =>
      (e.textContent || "").trim(),
    );
    // 4096 MB lands on an m7i.large, and the whole 8 GiB is usable.
    expect(units).toContain("→ m7i.large（2 vCPU / 8 GiB・専有）");
    // The disk is stated to be the persistent home, not the work disk.
    expect(units.some((u) => u.includes("home の作成時にだけ反映され"))).toBe(true);
    expect(units.some((u) => u.includes("作業ディスクは停止すると消えます"))).toBe(false);
  });
});

// The machine class (docs/log/70 §70.10). Three things:
//   1. A deployment with a single class shows no picker (never ask a question with one answer).
//   2. Changing the class redraws the memory chips from that class's ladder and recomputes the
//      instance landed on: the same MB figure lands on a different instance in another class.
//   3. The warning about rebuilding home appears only when the CPU architecture changes.
const SIZING_CLASSES = {
  ...SIZING_EC2,
  default_slot_class: "standard",
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
    {
      id: "big",
      label: "大きい（Intel）",
      arch: "x86_64",
      slots: [{ instance_type: "m7i.2xlarge", mem_mib: 32768, vcpu: 8 }],
    },
  ],
};

describe("member machine class", () => {
  const useSizing = (s: unknown) =>
    api.mockImplementation((p: string) =>
      p === "api/admin/workspace-sizing" ? Promise.resolve(s) : Promise.resolve({ running: false, sessions: [] }),
    );

  it("shows no picker at all on a deployment with only one class", async () => {
    useSizing(SIZING_EC2);
    await mount();
    await openEditor();
    expect(buttonWith("テナントの既定")).toBeUndefined();
  });

  it("switches the memory ladder and the instance landed on to the chosen class", async () => {
    useSizing(SIZING_CLASSES);
    await mount();
    await openEditor();

    // The ladder of the default class (the tenant default, standard).
    const chips = () =>
      Array.from(document.querySelectorAll<HTMLButtonElement>(".limit-edit .le-presets")).at(-1)!;
    expect(Array.from(chips().querySelectorAll(".chip")).map((b) => (b.textContent || "").trim())).toEqual([
      "8 GiB",
      "16 GiB",
    ]);
    let units = Array.from(document.querySelectorAll(".limit-edit .af-unit")).map((e) => (e.textContent || "").trim());
    expect(units).toContain("→ m7i.large（2 vCPU / 8 GiB・専有）");

    await act(async () => buttonWith("省コスト（Arm）")!.click());
    // The same 4096 MB lands on an m7g.large in the arm class.
    units = Array.from(document.querySelectorAll(".limit-edit .af-unit")).map((e) => (e.textContent || "").trim());
    expect(units).toContain("→ m7g.large（2 vCPU / 8 GiB・専有）");

    // Moving to a class with a different number of steps swaps the chip row too.
    await act(async () => buttonWith("大きい（Intel）")!.click());
    expect(Array.from(chips().querySelectorAll(".chip")).map((b) => (b.textContent || "").trim())).toEqual(["32 GiB"]);
  });

  it("sends slot_class on save", async () => {
    useSizing(SIZING_CLASSES);
    await mount();
    await openEditor();
    await act(async () => buttonWith("省コスト（Arm）")!.click());
    await act(async () => buttonWith("保存")!.click());
    const [, , body] = apiJSON.mock.calls.find((c) => c[0] === "api/admin/user-limits")!;
    expect(body).toMatchObject({ slot_class: "arm" });
  });

  // Home is rebuilt only when the architecture changes. Warning on a class change within the
  // same architecture reads as "something breaks every time" and stops being heeded on the one
  // occasion something really does break.
  it("warns only when the CPU architecture changes", async () => {
    useSizing(SIZING_CLASSES);
    await mount();
    await openEditor();
    const warned = () => !!document.querySelector(".limit-edit .admin-hint.warn");
    expect(warned()).toBe(false);

    await act(async () => buttonWith("大きい（Intel）")!.click()); // x86_64 → x86_64
    expect(warned()).toBe(false);

    await act(async () => buttonWith("省コスト（Arm）")!.click()); // x86_64 → arm64
    expect(warned()).toBe(true);
  });
});

// `member` is a snapshot taken when its row was clicked — the parent never refreshes it
// (onChanged reloads the tenant list, not the selection). Re-seeding the editor from that prop
// after a save shows the values from before the save, which on the machine chips reads as "the
// setting did not save" while it very much did. Measured on a live deployment
// (docs/log/70 §70.14.6).
describe("saved values survive reopening the editor", () => {
  beforeEach(() => {
    api.mockImplementation((p: string) =>
      p === "api/admin/workspace-sizing" ? Promise.resolve(SIZING_CLASSES) : Promise.resolve({ running: false, sessions: [] }),
    );
    apiJSON.mockImplementation((p: string, _m?: string, b?: Record<string, unknown>) =>
      p === "api/admin/user-limits" ? Promise.resolve({ slot_class: b?.slot_class }) : Promise.resolve({}),
    );
  });

  it("shows the saved machine class and numbers when reopened after a save", async () => {
    await mount();
    await openEditor();
    await act(async () => buttonWith("省コスト（Arm）")!.click());
    // Numbers are moved through the chips: assigning .value directly on a controlled input
    // does not update React's value tracker, so it is not picked up as a change.
    await act(async () => buttonWith("16 GiB")!.click());
    await act(async () => buttonWith("保存")!.click());

    // Reopen. The member prop is still stale, which is where implementations diverge.
    await openEditor();
    expect(buttonWith("省コスト（Arm）")!.className).toContain("on");
    expect(buttonWith("テナントの既定")!.className).not.toContain("on");
    expect(numbers()[1]).toBe("16384");
  });

  it("stops showing the architecture-change warning right after a save", async () => {
    await mount();
    await openEditor();
    await act(async () => buttonWith("省コスト（Arm）")!.click());
    expect(!!document.querySelector(".limit-edit .admin-hint.warn")).toBe(true);
    await act(async () => buttonWith("保存")!.click());
    await openEditor();
    // It is already arm, so on reopening there is nothing that would change.
    expect(!!document.querySelector(".limit-edit .admin-hint.warn")).toBe(false);
  });
});

// The resource tiles (docs/log/63 §63.9). On an ECS setup the measurements come from the Agent,
// which is what recovers from all three tiles reading "–" because the host cgroup is unreadable.
// What is pinned: an axis that cannot be measured is never drawn as 0, and the denominator of
// the percentage.
describe("workspace resource tiles", () => {
  const tiles = () =>
    Array.from(document.querySelectorAll<HTMLElement>(".res-tiles .res-tile")).map((t) => ({
      value: t.querySelector(".rt-value")?.textContent ?? "",
      sub: t.querySelector(".rt-sub")?.textContent ?? "",
    }));

  const withStats = (stats: Record<string, unknown>) =>
    api.mockImplementation((p: string) =>
      p.endsWith("/stats") ? Promise.resolve(stats) : Promise.resolve({ sessions: [] }),
    );

  it("draws the measured memory, CPU and disk values", async () => {
    withStats({
      running: true,
      mem_used: 2 * 1024 ** 3,
      mem_max: 8 * 1024 ** 3,
      cpu_pct: 42,
      disk_used: 20 * 1024 ** 3,
      disk_total: 40 * 1024 ** 3,
    });
    await mount();
    const [mem, cpu, disk] = tiles();
    expect(mem.value).toBe("2.00G");
    expect(mem.sub).toContain("8.00G");
    expect(cpu.value).toBe("42%");
    expect(disk.value).toBe("20.0G");
    // The denominator is the measured capacity; the point of docs/log/63 §63.9 is to remove
    // "running, yet still showing –".
    expect(disk.sub).toContain("/ 40.0G");
    expect(disk.sub).toContain("50%");
  });

  // For the denominator the measured value (disk_total) wins over the configured one
  // (disk_quota): ecs-ec2 applies disk_gb only at creation, so a number edited afterwards makes
  // the configured value a lie.
  it("prefers the measured capacity over the configured limit", async () => {
    withStats({
      running: true,
      disk_used: 30 * 1024 ** 3,
      disk_total: 60 * 1024 ** 3,
      disk_quota: 40 * 1024 ** 3,
    });
    await mount();
    expect(tiles()[2].sub).toContain("/ 60.0G");
  });

  // Setups without a measurement (docker: du plus a display-only quota) keep the configured
  // value as the denominator.
  it("falls back to the configured limit as the denominator without a measurement", async () => {
    withStats({ running: true, disk_used: 10 * 1024 ** 3, disk_quota: 40 * 1024 ** 3 });
    await mount();
    expect(tiles()[2].sub).toContain("/ 40.0G");
    expect(tiles()[2].sub).toContain("25%");
  });

  // An axis that could not be measured stays "–"; writing 0 hides the fact that nothing was
  // measured.
  it("leaves an unmeasured axis as – rather than 0", async () => {
    withStats({ running: true, mem_used: 1024 ** 3 });
    await mount();
    const [, cpu, disk] = tiles();
    expect(cpu.value).toBe("–");
    expect(disk.value).toBe("–");
  });
});
