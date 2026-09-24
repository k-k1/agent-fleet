// The machine section. What matters is that the two halves of the answer stay two answers:
//   1. on the EC2 slot pool every row is drawn, from the measurement, with the class named
//   2. with no SMBIOS reading the instance type still appears — from the configuration —
//      and says so, rather than the row vanishing
//   3. when the running box is not the configured one, BOTH are on screen
//   4. on a shared host the box's own RAM is never presented as the member's
//   5. stopped: it says these are next-start values instead of showing nothing
//   6. the usage charts read their ceilings from the machine above (memory limit, core
//      count), which is the reason they are on this tab and not only in the WS bar
//   7. the home disk figure is only called "yours" (and only tinted) on a box of your own;
//      elsewhere it is the whole filesystem
//   8. the disk breakdown is what Agent Fleet stores, and its button swaps Settings for the
//      cleanup modal
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MachineView } from "./MachineTab.tsx";
import type { WsMachine } from "../../../lib/machine.ts";
import { __test as feed } from "../../../core/store/wsStatsFeed.ts";

// Per-path answers; anything unlisted gets {} (what an Agent without the endpoint amounts to).
const answers = vi.hoisted(() => ({ byPath: {} as Record<string, unknown>, calls: [] as string[] }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (path: string, opts?: RequestInit) => {
    answers.calls.push((opts?.method ?? "GET") + " " + path);
    return Promise.resolve(answers.byPath[path] ?? {});
  },
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
  answers.byPath = {};
  answers.calls = [];
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

  const shared: WsMachine = {
    runtime: "docker",
    running: true,
    measured: { arch: "x86_64", vcpu: 8, mem_max: 10737418240, disk_total: 500 * 2 ** 30 },
  };
  const nearlyFull = () => {
    feed.setLatest({ running: true, mem_used: 1, mem_max: 10737418240, cpu_pct: 5, disk_used: 475 * 2 ** 30, disk_total: 500 * 2 ** 30 });
    feed.sample();
  };

  it("calls a shared host's disk the whole filesystem, and does not tint it", async () => {
    nearlyFull();
    const text = await mount(shared);
    expect(text).toContain("ディスク全体");
    expect(text).not.toContain("home ディスク");
    const bar = [...host!.querySelectorAll(".mu-chart")].find((el) => el.querySelector(".mu-bar"))!;
    expect(bar.className).not.toMatch(/is-(warn|crit)/);
  });

  it("calls a dedicated box's disk its own, and warns when it fills", async () => {
    // The positive control for the test above: same 95%, own box — label and tint both flip.
    nearlyFull();
    const text = await mount(ec2);
    expect(text).toContain("home ディスク");
    expect(text).not.toContain("ディスク全体");
    const bar = [...host!.querySelectorAll(".mu-chart")].find((el) => el.querySelector(".mu-bar"))!;
    expect(bar.className).toContain("is-crit");
  });

  it("breaks down what Agent Fleet stores and opens the cleanup in place of Settings", async () => {
    answers.byPath["api/cleanup/usage"] = {
      cache: {
        bytes: 1900 * 2 ** 20,
        files: 9000,
        parts: [
          { name: "generated", bytes: 1500 * 2 ** 20, files: 1400 },
          { name: "codex-view-image", bytes: 102 * 2 ** 20, files: 96 },
          { name: "empty", bytes: 0, files: 0 },
        ],
      },
      orphans: { ok: true, bytes: 102 * 2 ** 20, files: 344, dirs: 125 },
      trash: { bytes: 486 * 2 ** 20, archives: 1082, oldest: "2026-07-26" },
    };
    const { useSettingsUI } = await import("../store.ts");
    const { useSessionUI } = await import("../../sessions/ui.ts");
    useSettingsUI.setState({ settingsOpen: true });
    useSessionUI.setState({ cleanupOpen: false });

    const text = await mount(shared);
    expect(text).toContain("Agent Fleet が使っているディスク");
    expect(text).toContain("生成した画像");
    expect(text).toContain("1.5 GB");
    expect(text).toContain("codex の画像");
    expect(text).not.toContain("empty"); // a zero-byte part is not a line item
    expect(text).toContain("うち削除済みの分");
    expect(text).toContain("486 MB（1082 件）・最古 2026-07-26");

    const btn = [...host!.querySelectorAll("button")].find((b) => b.textContent?.includes("掃除を開く"))!;
    await act(async () => {
      btn.click();
    });
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(useSessionUI.getState().cleanupOpen).toBe(true);
  });

  it("shows where each figure lives, opens it in the file tree, and picture folders in the gallery", async () => {
    answers.byPath["api/cleanup/usage"] = {
      cache: {
        bytes: 1600 * 2 ** 20,
        files: 9000,
        path: "~/.cache/agent-fleet",
        browse: ".cache/agent-fleet",
        parts: [
          { name: "generated", bytes: 1500 * 2 ** 20, files: 1400, path: "~/.cache/agent-fleet/generated", browse: ".cache/agent-fleet/generated" },
          { name: "thumbs", bytes: 100 * 2 ** 20, files: 900, path: "~/.cache/agent-fleet/thumbs", browse: ".cache/agent-fleet/thumbs" },
        ],
      },
      orphans: { ok: true, bytes: 0, files: 0, dirs: 0 },
      // Outside the browse root: readable, but nothing to open it with.
      trash: { bytes: 10, archives: 1, path: "/data/cleanup" },
    };
    const { useSettingsUI } = await import("../store.ts");
    const { useFilesStore } = await import("../../files/store.ts");
    const { useLayoutStore } = await import("../../../layout/store.ts");
    useSettingsUI.setState({ settingsOpen: true });
    const text = await mount(shared);
    expect(text).toContain("~/.cache/agent-fleet/generated");
    expect(text).toContain("/data/cleanup");

    const paths = [...host!.querySelectorAll<HTMLElement>(".mv-path")];
    expect(paths.find((el) => el.textContent === "/data/cleanup")!.tagName).toBe("SPAN"); // not a link
    const gen = paths.find((el) => el.textContent === "~/.cache/agent-fleet/generated")!;
    await act(async () => {
      gen.click();
    });
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(useFilesStore.getState().reveal).toMatchObject({ path: ".cache/agent-fleet/generated", focus: true });

    // A gallery button on the picture folders (thumbnails included) — not on the cache total.
    const galleryButtons = [...host!.querySelectorAll("button")].filter((b) => b.textContent?.includes("ギャラリーで開く"));
    expect(galleryButtons.length).toBe(2);
    useSettingsUI.setState({ settingsOpen: true });
    const openTarget = vi.fn();
    const before = useLayoutStore.getState().openTarget;
    useLayoutStore.setState({ openTarget });
    await act(async () => {
      galleryButtons[0].click();
    });
    useLayoutStore.setState({ openTarget: before });
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(openTarget).toHaveBeenCalledWith({ content: { kind: "gallery", galleryPath: ".cache/agent-fleet/generated" } });
  });

  it("says so when the Agent cannot tell what is still referenced", async () => {
    answers.byPath["api/cleanup/usage"] = {
      cache: { bytes: 10, files: 1, parts: [] },
      orphans: { ok: false, bytes: 0, files: 0, dirs: 0 },
      trash: { bytes: 0, archives: 0 },
    };
    const text = await mount(shared);
    expect(text).toContain("判定できません");
  });

  it("reports a failure instead of spinning when the Agent has no breakdown", async () => {
    const text = await mount(shared);
    expect(text).toContain("ディスクの内訳を取得できませんでした");
  });

  it("says the session share was not counted, rather than that there were too many files", async () => {
    answers.byPath["api/cleanup/usage"] = {
      cache: { bytes: 10, files: 1, parts: [] },
      orphans: { ok: true, bytes: 10, files: 1, dirs: 1, unjudged: 2 },
      trash: { bytes: 0, archives: 0 },
    };
    const text = await mount(shared);
    expect(text).toContain("チャットの分だけです");
    expect(text).not.toContain("ファイルが多すぎる");
  });

  it("measures the tool caches only on a press, and empties one after confirming", async () => {
    answers.byPath["api/cleanup/tool-caches"] = {
      caches: [
        { name: "go-build", bytes: 34 * 2 ** 30, files: 400000, path: "~/.cache/go-build" },
        { name: "npm", bytes: 41 * 2 ** 30, files: 900000, busy: [4242], path: "~/.npm/_cacache" },
      ],
    };
    answers.byPath["api/cleanup/tool-caches/go-build"] = { name: "go-build", bytes: 34 * 2 ** 30, files: 400000 };
    let text = await mount(shared);
    expect(text).toContain("ツールのキャッシュ");
    expect(answers.calls).not.toContain("GET api/cleanup/tool-caches"); // a walk of 10^5 files waits for the press

    const buttons = () => [...document.body.querySelectorAll("button")];
    await act(async () => {
      buttons().find((b) => b.textContent === "測る")!.click();
    });
    text = host!.textContent || "";
    expect(text).toContain("go-build");
    expect(text).toContain("使用中（pid 4242）");
    // Only go-build offers "empty": npm is in use.
    expect(buttons().filter((b) => b.textContent === "空にする")).toHaveLength(1);

    await act(async () => {
      buttons().find((b) => b.textContent === "空にする")!.click();
    });
    expect(document.body.querySelector(".confirm-title")?.textContent).toBe("go-build のキャッシュを空にしますか？");
    expect(answers.calls.some((c) => c.startsWith("DELETE"))).toBe(false);
    await act(async () => {
      (document.body.querySelector(".confirm-actions button:last-child") as HTMLButtonElement).click();
    });
    expect(answers.calls).toContain("DELETE api/cleanup/tool-caches/go-build");
    expect(host!.textContent).toContain("go-build を空にしました");
    expect(document.body.querySelector(".confirm-title")).toBeNull();
  });
});
