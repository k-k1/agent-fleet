// The unsynced-settings notice (#1023 item 1): hidden while saves land, shown once one fails, and
// its button sends the change again.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  apiMock.mockReset().mockResolvedValue({});
  apiJSONMock.mockReset().mockResolvedValue({});
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

describe("PrefsSyncBanner", () => {
  it("appears when a save fails and goes away once the retry lands", async () => {
    vi.resetModules();
    const settings = await import("../../lib/settings.ts");
    const { PrefsSyncBanner } = await import("./PrefsSyncBanner.tsx");
    await settings.hydrateUIPrefs();
    root = createRoot(host);
    await act(async () => root!.render(<PrefsSyncBanner />));
    expect(host.textContent).toBe("");

    apiJSONMock.mockResolvedValueOnce({ error: { code: "http_413" } });
    await act(async () => {
      settings.setSetting("chatSize", 17);
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(host.querySelector('[role="status"]')).not.toBeNull();

    await act(async () => {
      host.querySelector("button")!.click();
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(apiJSONMock).toHaveBeenCalledTimes(2);
    expect(host.textContent).toBe("");
  });
});
