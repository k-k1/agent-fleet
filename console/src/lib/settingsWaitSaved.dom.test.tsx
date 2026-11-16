// waitPrefsSaved against the real debounce and PUT: Reset my settings refreshes what the Agent
// shows only after this resolves true, so a save that landed must resolve true (not time out), and
// one that failed must resolve false.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

async function freshSettings() {
  localStorage.setItem("af-display-settings", "{}");
  vi.resetModules();
  return await import("./settings.ts");
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset().mockResolvedValue({});
  apiJSONMock.mockReset();
  localStorage.clear();
});
afterEach(() => vi.useRealTimers());

describe("waitPrefsSaved", () => {
  it("resolves true once the debounced PUT has landed", async () => {
    apiJSONMock.mockResolvedValue({});
    const s = await freshSettings();
    await s.hydrateUIPrefs();
    s.setSettings({ agentEnabled: {}, lcppEnabled: true });
    let result: boolean | undefined;
    void s.waitPrefsSaved(15_000).then((v) => (result = v));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    expect(result).toBe(true);
  });

  it("resolves false when the PUT is refused", async () => {
    apiJSONMock.mockResolvedValue({ error: { code: "http_502" } });
    const s = await freshSettings();
    await s.hydrateUIPrefs();
    s.setSettings({ lcppEnabled: true, agentEnabled: { codex: true } });
    let result: boolean | undefined;
    void s.waitPrefsSaved(15_000).then((v) => (result = v));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(result).toBe(false);
  });

  it("resolves false when nothing lands in time (the server copy was never read)", async () => {
    apiMock.mockResolvedValue({ error: { code: "http_502" } });
    const s = await freshSettings();
    await s.hydrateUIPrefs();
    s.setSettings({ lcppEnabled: true });
    let result: boolean | undefined;
    void s.waitPrefsSaved(5_000).then((v) => (result = v));
    await vi.advanceTimersByTimeAsync(6_000);
    expect(result).toBe(false);
    expect(apiJSONMock).not.toHaveBeenCalled();
  });
});
