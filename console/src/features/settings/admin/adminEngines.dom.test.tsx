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
