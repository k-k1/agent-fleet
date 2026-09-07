// The member's view of the on-demand engine, in the read-aloud settings tab (ADR 0070
// decisions 4 and 16).
//
// Before on-demand there was nothing to show: the engine either existed and ran, or did not
// exist. Now "stopped" is its normal state, and a member whose Japanese is suddenly read by
// Polly has to be able to tell three situations apart — it is warming up (wait a few
// seconds), it is not running (ask for it), an administrator switched it off (nothing you
// can do) — and to act on the middle one.
//
// It is tested through the tab rather than against the component, because the failure being
// guarded against is the wiring: a control that is never rendered fails silently and looks
// exactly like a deployment that has no engine.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { TtsStatus } from "../../chat/ttsAvailability.ts";

const wakeEngine = vi.fn();
const loadTtsStatus = vi.fn();
let status: TtsStatus | null = null;

vi.mock("../../chat/ttsStatus.ts", async () => {
  // engineActivity is the real one: what it decides is exactly what this screen is for.
  const real = await import("../../chat/ttsAvailability.ts");
  return {
    ...real,
    loadTtsStatus: () => loadTtsStatus(),
    refreshTtsStatus: () => loadTtsStatus(),
    ttsStatusCache: () => null,
    wakeEngine: () => wakeEngine(),
  };
});
// The character list and the audition button pull in the whole audio stack; neither is what
// this file is about.
vi.mock("../../chat/ttsSpeakers.ts", () => ({ loadSpeakers: async () => null, speakersCatalog: () => null }));
vi.mock("../../chat/tts.ts", () => ({
  voiceCharacters: () => [],
  isDefaultVoice: () => false,
  previewVoice: () => {},
}));

import { setLocale } from "../../../lib/i18n/index.ts";
import { setSettings } from "../../../lib/settings.ts";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { TtsTab } from "./TtsTab.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const managed = (v: Partial<TtsStatus["voicevox"]>): TtsStatus => ({
  voicevox: { ready: false, managed: true, mode: "ondemand", enabled: true, ...v },
  polly: { ready: true },
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ConfirmProvider>
        <TtsTab />
      </ConfirmProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const wakeButton = () =>
  Array.from(host!.querySelectorAll("button")).find((b) => b.textContent?.includes("ずんだもんを呼ぶ")) as
    | HTMLButtonElement
    | undefined;

beforeEach(() => {
  wakeEngine.mockReset();
  loadTtsStatus.mockReset();
  loadTtsStatus.mockImplementation(async () => status);
  // Through the store rather than localStorage: the settings module loads once, at import,
  // which is before any beforeEach runs.
  setSettings({ ttsEnabled: true, ttsProvider: "auto" }); // the voice section only exists when read-aloud is on
  setLocale("ja");
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  localStorage.clear();
});

describe("TtsTab engine state", () => {
  it("offers the button while the engine is stopped, and says Polly is reading", async () => {
    status = managed({ state: "stopped" });
    await mount();
    expect(host!.textContent).toContain("停止しています");
    expect(wakeButton()).toBeTruthy();

    wakeEngine.mockResolvedValue({ ok: true, started: true, message: "", status: managed({ state: "starting" }) });
    await act(async () => {
      wakeButton()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(wakeEngine).toHaveBeenCalledTimes(1);
    // The answer is applied straight away: the member pressed a button and a 70-second wait
    // that still says "stopped" reads as a button that did nothing.
    expect(host!.textContent).toContain("起動しています");
    expect(wakeButton()).toBeFalsy();
  });

  // ECS RUNNING is not readiness (decision 16): /version answers before a voice model is
  // loaded. Offering the button here would call an engine that is already there.
  it("says it is warming up when the container runs but is not ready", async () => {
    status = managed({ state: "running", ready: false });
    await mount();
    expect(host!.textContent).toContain("最初の声の準備中");
    expect(wakeButton()).toBeFalsy();
  });

  it("shows nothing at all once the engine is ready", async () => {
    status = managed({ state: "running", ready: true });
    await mount();
    expect(host!.textContent).not.toContain("Polly が代読");
    expect(wakeButton()).toBeFalsy();
  });

  // A member cannot buy their way past an administrator's decision, and starting a task
  // whose routing is off would pay for silence.
  it("does not offer the button while an administrator has it switched off", async () => {
    status = managed({ state: "stopped", mode: "off", enabled: false });
    await mount();
    expect(host!.textContent).toContain("管理者が無効にしています");
    expect(wakeButton()).toBeFalsy();
  });

  it("shows nothing where the deployment has no engine", async () => {
    status = { voicevox: { ready: false }, polly: { ready: true } };
    await mount();
    expect(wakeButton()).toBeFalsy();
  });

  it("surfaces a refusal instead of pretending the engine is coming", async () => {
    status = managed({ state: "stopped" });
    await mount();
    wakeEngine.mockResolvedValue({ ok: false, started: false, message: "呼び出せません（上限）", status: null });
    await act(async () => {
      wakeButton()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(host!.textContent).toContain("呼び出せません（上限）");
  });
});
