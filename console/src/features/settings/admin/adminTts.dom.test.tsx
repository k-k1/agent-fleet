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
const setTenantDict = vi.fn();
vi.mock("../../chat/ttsDict.ts", () => ({ setTenantDict: (...a: unknown[]) => setTenantDict(...a) }));

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
  setTenantDict.mockReset();
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
    expect(seg("オンデマンド")!.className).toContain("seg-btn--active");
    expect(host!.textContent).toContain("オンデマンド:"); // the note explaining what it will do
  });

  it("says stopping — not enabled — while the undo window runs", async () => {
    api.mockResolvedValue(status("ondemand", "running", true));
    apiJSON.mockResolvedValue(status("off", "stopping", true));
    await mount();
    await click(seg("無効"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/tts", "PUT", { mode: "off" });
    expect(seg("無効")!.className).toContain("seg-btn--active");
    expect(seg("常時稼働")!.className).not.toContain("seg-btn--active");
    expect(host!.textContent).toContain("停止処理中");
    expect(host!.textContent).not.toContain("稼働中");
  });

  it("hides on-demand where nothing can start an engine (an externally run one)", async () => {
    api.mockResolvedValue({ managed: false, mode: "on", enabled: true, engine: { ready: true }, polly: { ready: false }, dict: "" });
    await mount();
    expect(seg("オンデマンド")).toBeUndefined();
    expect(seg("常時稼働")!.className).toContain("seg-btn--active");
  });

  it("treats a null body from the dictionary PUT as saved, as before: applies it here and clears dirty", async () => {
    api.mockResolvedValue(status("ondemand", "running", true));
    apiJSON.mockResolvedValue(null);
    await mount();
    const area = host!.querySelector("textarea.ds-userdict") as HTMLTextAreaElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "foo=bar");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const save = Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === "保存") as HTMLButtonElement;
    expect(save.disabled).toBe(false);
    await click(save);
    expect(setTenantDict).toHaveBeenCalledWith("foo=bar");
  });

  it("drops a poll read that was in flight when a mode change landed", async () => {
    let answerPoll: (v: unknown) => void = () => {};
    api
      .mockResolvedValueOnce(status("ondemand", "running", true))
      .mockImplementationOnce(() => new Promise((r) => (answerPoll = r)));
    apiJSON.mockResolvedValue(status("off", "stopping", true));
    await mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5100); // the poll's read is now in flight
    });
    expect(api).toHaveBeenCalledTimes(2);
    await click(seg("無効"));
    expect(seg("無効")!.className).toContain("active");
    await act(async () => {
      answerPoll(status("ondemand", "running", true)); // older than the mode change
      await Promise.resolve();
    });
    expect(seg("無効")!.className).toContain("active");
    expect(host!.textContent).toContain("停止処理中");
  });

  it("drops a poll read begun while a mode change was in flight and answered after it", async () => {
    let answerPut: (v: unknown) => void = () => {};
    let answerPoll: (v: unknown) => void = () => {};
    api
      .mockResolvedValueOnce(status("ondemand", "running", true))
      .mockImplementationOnce(() => new Promise((r) => (answerPoll = r)));
    apiJSON.mockImplementationOnce(() => new Promise((r) => (answerPut = r)));
    await mount();
    await click(seg("無効")); // the PUT is in flight
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5100); // a poll read begins while it is
    });
    expect(api).toHaveBeenCalledTimes(2);
    await act(async () => {
      answerPut(status("off", "stopping", true));
      await Promise.resolve();
    });
    await act(async () => {
      answerPoll(status("ondemand", "running", true)); // served before the server applied the PUT
      await Promise.resolve();
    });
    expect(seg("無効")!.className).toContain("active");
    expect(host!.textContent).toContain("停止処理中");
  });

  it("keeps a saved dictionary saved when an older poll read answers after the save", async () => {
    let answerPoll: (v: unknown) => void = () => {};
    api
      .mockResolvedValueOnce(status("ondemand", "running", true))
      .mockImplementationOnce(() => new Promise((r) => (answerPoll = r)));
    apiJSON.mockResolvedValue({ ...status("ondemand", "running", true), dict: "foo=bar" });
    await mount();
    const area = host!.querySelector("textarea.ds-userdict") as HTMLTextAreaElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "foo=bar");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5100); // a poll read is in flight
    });
    const save = () => Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === "保存") as HTMLButtonElement;
    await click(save());
    expect(save().disabled).toBe(true);
    await act(async () => {
      answerPoll(status("ondemand", "running", true)); // still the empty dictionary
      await Promise.resolve();
    });
    expect(save().disabled).toBe(true);
  });

  it("a mode change answering first keeps its mode and still takes the dictionary an older read brings", async () => {
    let answerPoll: (v: unknown) => void = () => {};
    api
      .mockResolvedValueOnce(status("ondemand", "running", true))
      .mockImplementationOnce(() => new Promise((r) => (answerPoll = r)));
    apiJSON.mockResolvedValue({ ...status("on", "running", true), dict: "x=y" });
    await mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5100); // the read is in flight with another admin's dictionary
    });
    await click(seg("常時稼働")); // settled and pinned on: the poll stops
    await act(async () => {
      answerPoll({ ...status("ondemand", "running", true), dict: "x=y" });
      await Promise.resolve();
    });
    expect(seg("常時稼働")!.className).toContain("active");
    // The saved baseline moved to x=y while the box still holds the untouched "" draft, so the
    // panel now reads as unsaved, exactly as main did.
    const save = Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === "保存") as HTMLButtonElement;
    expect(save.disabled).toBe(false);
  });

  it("discards the old state when a re-read answers a null body", async () => {
    api.mockResolvedValueOnce(status("ondemand", "starting")).mockResolvedValue(null);
    await mount();
    expect(seg("オンデマンド")).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5100);
    });
    expect(seg("オンデマンド")).toBeFalsy();
  });
});
