// The "learned suggestions" block of the keyboard settings. Learning grows silently on every
// send, so if the only way to clean up were "clear all", tidying away a one-off phrasing would
// take the frequently used suggestions with it. What is pinned down here:
//   1. When one-off suggestions exist, a button appears carrying their count (and not
//      otherwise).
//   2. Pressing it removes only the one-off suggestions; frequent ones and pins survive.
//   3. The hidden list does not grow (sending the text again re-learns it; this is not
//      "never show this again").
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));

import { KeysTab } from "./KeysTab.tsx";
import { getSettings, setSetting } from "../../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<KeysTab />);
  });
}

// Look the buttons up by class so the test does not depend on the display language; only the
// count is read from the label.
const btn = (cls: string) => document.querySelector<HTMLButtonElement>(".qr-learned ." + cls);

beforeEach(() => {
  api.mockReset().mockResolvedValue({});
  apiJSON.mockReset().mockResolvedValue({});
  localStorage.clear();
  setSetting("quickRepliesHidden", []);
  setSetting("quickRepliesPinned", []);
  setSetting("quickReplies", {});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("KeysTab learned quick replies", () => {
  it("clears the one-off suggestions in bulk with their count, leaving frequent, pinned and hidden alone", async () => {
    setSetting("quickReplies", {
      ok: { text: "OK", count: 7, at: 30 },
      "後で": { text: "後で", count: 1, at: 20 },
      "あとで見る": { text: "あとで見る", count: 1, at: 10 },
      "ピン留めの一言": { text: "ピン留めの一言", count: 1, at: 5 },
    });
    setSetting("quickRepliesPinned", ["ピン留めの一言"]);
    setSetting("quickRepliesHidden", ["やめて"]);
    await mount();

    const clear = btn("qr-clear-once")!;
    expect(clear).toBeTruthy();
    expect(clear.textContent).toContain("2"); // the pinned one-off is not counted
    await act(async () => {
      clear.click();
    });

    expect(Object.keys(getSettings().quickReplies)).toEqual(["ok", "ピン留めの一言"]);
    expect(getSettings().quickRepliesPinned).toEqual(["ピン留めの一言"]);
    expect(getSettings().quickRepliesHidden).toEqual(["やめて"]); // the hidden list does not grow
    expect(btn("qr-clear-once")).toBeNull(); // with nothing left to clear, the button goes too
  });

  it("hides the button when there is no one-off suggestion (clear all stays)", async () => {
    setSetting("quickReplies", { ok: { text: "OK", count: 3, at: 30 } });
    await mount();
    expect(btn("qr-clear-once")).toBeNull();
    expect(btn("qr-clear-all")).toBeTruthy();
  });
});
