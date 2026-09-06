// The VOICEVOX engine panel under on-demand management (ADR 0070 decision 7).
//
// What is pinned here is that the panel reports two different things: the mode the
// administrator chose, and what the deployment is doing about it. They disagree on purpose
// for about a minute after "off" is pressed — the stop is debounced so that pressing off
// and on again costs no cold start — and a panel that kept saying "enabled" through that
// window would be reporting the opposite of the intent. That is exactly the defect class
// this screen was audited for once already: a setting shown as though it were reality.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
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
vi.mock("../../chat/ttsDict.ts", () => ({ setTenantDict: () => {} }));

import { TtsAdminView } from "./adminTts.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const status = (mode: string, state: string, ready = false) => ({
  managed: true,
  mode,
  enabled: mode !== "off",
  engine: { ready, state },
  polly: { ready: true },
  dict: "",
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TtsAdminView />);
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

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("TtsAdminView", () => {
  it("offers three modes under management and marks the stored one", async () => {
    api.mockResolvedValue(status("ondemand", "stopped"));
    await mount();
    expect(seg("無効")).toBeTruthy();
    expect(seg("オンデマンド")).toBeTruthy();
    expect(seg("常時稼働")).toBeTruthy();
    expect(seg("オンデマンド")!.className).toContain("active");
    expect(host!.textContent).toContain("オンデマンド:"); // the note explaining what it will do
  });

  it("says stopping — not enabled — while the undo window runs", async () => {
    api.mockResolvedValue(status("ondemand", "running", true));
    apiJSON.mockResolvedValue(status("off", "stopping", true));
    await mount();
    await click(seg("無効"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tts", "PUT", { mode: "off" });
    expect(seg("無効")!.className).toContain("active");
    expect(seg("常時稼働")!.className).not.toContain("active");
    expect(host!.textContent).toContain("停止処理中");
    expect(host!.textContent).not.toContain("稼働中");
  });

  it("hides on-demand where nothing can start an engine (an externally run one)", async () => {
    api.mockResolvedValue({ managed: false, mode: "on", enabled: true, engine: { ready: true }, polly: { ready: false }, dict: "" });
    await mount();
    expect(seg("オンデマンド")).toBeUndefined();
    expect(seg("常時稼働")!.className).toContain("active");
  });
});
