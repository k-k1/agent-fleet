// The self-hosted engine panel (ADR 0071).
//
// What is pinned here is the same distinction the VOICEVOX panel was audited for: the MODE is
// the administrator's intent and the STATE is what ECS is doing about it, and for the minute
// after "off" they disagree. A panel that echoed ECS back would report the opposite of the
// button just pressed.
//
// Plus the thing that is specific to this screen: a GPU box is $1.26/hour, so "always on" has
// to be visibly different from the other two rather than just another segment.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));

import { EnginesAdminView } from "./adminEngines.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const row = (over: Record<string, unknown> = {}) => ({
  key: "image",
  api: "images",
  provider: "sdcpp",
  models: ["sdxl-base-1.0"],
  mode: "ondemand",
  enabled: true,
  managed: true,
  state: "stopped",
  desired: 0,
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnginesAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const seg = (label: string) =>
  Array.from(host!.querySelectorAll(".seg-btn")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;

const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  vi.useRealTimers();
});

describe("EnginesAdminView", () => {
  it("switches an engine off through the per-engine route", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue(row({ mode: "off", enabled: false, state: "stopping" }));
    await mount();

    await click(seg("無効"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image", "PUT", { mode: "off" });
    // The answer, not a re-fetch, is what the row becomes — the same reason the TTS panel
    // does it: a poll landing in between would show the pre-click state.
    expect(host!.textContent).toContain("停止処理中");
  });

  it("shows the mode the admin chose, not what ECS is still doing", async () => {
    // The disagreement window: mode off, task still going away.
    api.mockResolvedValue({ engines: [row({ mode: "off", enabled: false, state: "stopping" })] });
    await mount();
    expect(seg("無効")?.className).toContain("active");
    expect(seg("常時稼働")?.className).not.toContain("active");
  });

  it("warns about the bill only while an engine is pinned on", async () => {
    api.mockResolvedValue({ engines: [row({ mode: "ondemand" })] });
    await mount();
    expect(host!.textContent).not.toContain("$1.26");

    apiJSON.mockResolvedValue(row({ mode: "on", state: "running" }));
    await click(seg("常時稼働"));
    expect(host!.textContent).toContain("$1.26");
  });

  it("says so rather than showing an empty screen when nothing is deployed", async () => {
    api.mockResolvedValue({ engines: [] });
    await mount();
    expect(host!.querySelectorAll(".seg-btn").length).toBe(0);
    expect(host!.textContent).toContain("動かしていません");
  });

  // The status block, and the rule it is written to: a field the CP has no answer for is
  // ABSENT, and the panel must not fill the hole. Each assertion below is a hole that would
  // otherwise be filled with something an operator would act on.
  it("shows when the box started and when it will stop by itself", async () => {
    const now = Date.now();
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          warm: true,
          // The BOX's own clock, not the service's: `service_since` moves on a stack update
          // without a new box being bought, and it is the box that costs $1.26/hour.
          box: { id: "i-08a9", status: "ACTIVE", since: new Date(now - 3720_000).toISOString() },
          service_since: new Date(now - 99_000_000).toISOString(),
          stop_eta: new Date(now + 600_000).toISOString(),
          window_secs: 300,
          window_counted_secs: 300,
          window_units: 3,
          last_demand: new Date(now - 120_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("i-08a9");
    expect(text).toContain("1 時間 2 分 経過"); // the box's age, not the service's
    expect(text).toContain("あと 10 分");
    expect(text).toContain("直近 5 分の要求: 3 件");
    expect(text).toContain("（読み込み済）");
    // The count covers the whole window, so it must NOT be qualified — a warning that is
    // always on is a warning nobody reads.
    expect(text).not.toContain("この CP が数えているのは");
  });

  it("does not promise a stop for an engine that will not stop", async () => {
    // Pinned on. The CP omits stop_eta, and the panel must not substitute anything for it —
    // not even the idle policy, which does not apply while the engine is pinned.
    api.mockResolvedValue({
      engines: [
        row({
          mode: "on",
          state: "running",
          desired: 1,
          idle_secs: 900,
          window_secs: 300,
          window_counted_secs: 300,
        }),
      ],
    });
    await mount();
    expect(host!.textContent).not.toContain("自動停止");
  });

  it("states the idle window for a stopped on-demand engine, as a policy and not a time", async () => {
    // Nothing to stop, so there is no countdown — but the window itself is still worth knowing,
    // and it is a different claim ("30 minutes after the last request") from a clock time.
    api.mockResolvedValue({
      engines: [row({ mode: "ondemand", state: "stopped", desired: 0, idle_secs: 900 })],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("誰も使わなくなってから 15 分で自動停止します");
    expect(text).not.toContain("あと");
  });

  // 🔴 The one number on this panel that can be confidently wrong. The rolling count lives in
  // the control plane's memory, so a CP replaced two minutes ago answers "0 requests in the
  // last 5 minutes" while somebody is mid-conversation with the engine.
  it("says so when it has not been counting for a whole window", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          window_secs: 300,
          window_counted_secs: 90,
          window_units: 0,
          last_demand: new Date(Date.now() - 60_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("直近 5 分の要求: 0 件");
    expect(text).toContain("この CP が数えているのは");
    // The last-request time is persisted, so it stays true across the restart the count did
    // not survive — which is what makes the 0 above readable rather than alarming.
    expect(text).toContain("最後の要求");
  });

  // The service events are the only place ECS writes down why a start failed, and an engine
  // stuck in `starting` is exactly when somebody needs them.
  it("shows why a start is stuck, only while it is stuck", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "starting",
          desired: 1,
          events: ["(service af-image) was unable to place a task because no container instance met all of its requirements."],
        }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("no container instance met all of its requirements");
  });

  // <details> hides its children, it does not unmount them. Leaving the heatmap inside a closed
  // one fires a 14-day query per engine on load, for a section nobody opened.
  it("does not fetch the history until the section is opened", async () => {
    api.mockResolvedValue({ engines: [row(), row({ key: "llm" })] });
    await mount();
    expect(api.mock.calls.map((c) => String(c[0]))).toEqual(["api/admin/engines"]);
  });

  it("lists every engine, each with its own control", async () => {
    api.mockResolvedValue({
      engines: [
        row({ key: "llm", api: "chat", provider: "llamacpp", models: ["qwen3-coder-30b-a3b"] }),
        row(),
      ],
    });
    await mount();
    expect(host!.querySelectorAll(".admin-panel").length).toBe(2);
    // Three segments each: off / on-demand / always-on.
    expect(host!.querySelectorAll(".seg-btn").length).toBe(6);
    expect(host!.textContent).toContain("qwen3-coder-30b-a3b");
    expect(host!.textContent).toContain("sdxl-base-1.0");
  });
});
