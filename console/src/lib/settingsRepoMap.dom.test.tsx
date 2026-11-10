// #3775: the per-project repository memory merges per entry across tabs and devices, and a
// forgotten entry is not brought back by the merge.
import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";

async function fresh(local: Record<string, unknown> = {}) {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  vi.resetModules();
  const s = await import("./settings.ts");
  const m = await import("./settings/repoMap.ts");
  return { s, m };
}

const putBody = () => {
  const calls = apiJSONMock.mock.calls.filter((c) => c[1] === "PUT");
  return calls[calls.length - 1][2] as { workItemRepoMap: Record<string, { repo: string; at: number }> };
};

beforeAll(async () => {
  await import("./settings.ts"); // warm the transform before the timed cases
}, 60_000);

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});
afterEach(() => vi.useRealTimers());

// Every case rebuilds the settings module graph, which is slow on a loaded host (see
// settingsEquivalence.dom.test.tsx).
describe("workItemRepoMap merge", { timeout: 60_000 }, () => {
  it("a stale tab's save keeps the entry another tab saved meanwhile", async () => {
    apiMock.mockResolvedValueOnce({});
    const { s, m } = await fresh();
    await s.hydrateUIPrefs();
    // Meanwhile tab A saved jira:CPAAS; this tab never saw it.
    apiMock.mockResolvedValue({ workItemRepoMap: { "jira:CPAAS": { repo: "web", at: 100 } } });
    s.setSetting("workItemRepoMap", m.withRepoEntry(s.getSettings().workItemRepoMap, "jira:OTHER", "sdk", 200));
    await vi.advanceTimersByTimeAsync(1_000);
    const sent = putBody().workItemRepoMap;
    expect(m.liveRepoMap(sent)).toEqual({ "jira:CPAAS": "web", "jira:OTHER": "sdk" });
    expect(m.liveRepoMap(s.getSettings().workItemRepoMap)).toEqual({ "jira:CPAAS": "web", "jira:OTHER": "sdk" });
  });

  it("a forgotten entry is not resurrected by the server's older copy, on save or hydrate", async () => {
    const entry = { "jira:CPAAS": { repo: "web", at: 100 } };
    apiMock.mockResolvedValueOnce({ workItemRepoMap: entry });
    const { s, m } = await fresh();
    await s.hydrateUIPrefs();
    s.setSetting("workItemRepoMap", m.withRepoEntry(s.getSettings().workItemRepoMap, "jira:CPAAS", "", 200));
    apiMock.mockResolvedValue({ workItemRepoMap: entry }); // the server still holds the entry
    await vi.advanceTimersByTimeAsync(1_000);
    expect(putBody().workItemRepoMap["jira:CPAAS"]).toEqual({ repo: "", at: 200 });
    expect(m.liveRepoMap(s.getSettings().workItemRepoMap)).toEqual({});
    // Another hydrate with the stale server copy leaves it forgotten and pushes the tombstone.
    apiJSONMock.mockClear();
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(m.liveRepoMap(s.getSettings().workItemRepoMap)).toEqual({});
    expect(putBody().workItemRepoMap["jira:CPAAS"].repo).toBe("");
  });

  it("hydrate unions this device's entries with the server's and restores the missing ones", async () => {
    apiMock.mockResolvedValueOnce({ workItemRepoMap: { "jira:B": { repo: "sdk", at: 2 } } });
    const { s, m } = await fresh({ workItemRepoMap: { "jira:A": "web" } }); // legacy string form
    await s.hydrateUIPrefs();
    expect(m.liveRepoMap(s.getSettings().workItemRepoMap)).toEqual({ "jira:A": "web", "jira:B": "sdk" });
    await vi.advanceTimersByTimeAsync(1_000);
    expect(m.liveRepoMap(putBody().workItemRepoMap)).toEqual({ "jira:A": "web", "jira:B": "sdk" });
  });

  it("still sends the save when the pre-save read fails or hangs", async () => {
    apiMock.mockResolvedValueOnce({});
    const { s, m } = await fresh();
    await s.hydrateUIPrefs();
    apiMock.mockRejectedValueOnce(new Error("offline"));
    s.setSetting("workItemRepoMap", m.withRepoEntry(s.getSettings().workItemRepoMap, "jira:A", "web", 200));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(m.liveRepoMap(putBody().workItemRepoMap)).toEqual({ "jira:A": "web" });

    apiJSONMock.mockClear();
    apiMock.mockReturnValueOnce(new Promise(() => {})); // never answers
    s.setSetting("workItemRepoMap", m.withRepoEntry(s.getSettings().workItemRepoMap, "jira:B", "sdk", 300));
    await vi.advanceTimersByTimeAsync(700);
    expect(apiJSONMock).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(m.liveRepoMap(putBody().workItemRepoMap)).toEqual({ "jira:A": "web", "jira:B": "sdk" });
  });
});
