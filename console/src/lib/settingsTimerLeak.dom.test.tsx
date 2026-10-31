// Guard for src/test/prefsTimersSetup.ts: a ui-prefs timer scheduled by one test must not
// outlive it (#2015). The pair below reproduces the leak with real timers, as the full suite
// meets it: test 1 leaves the 600 ms debounced save pending and ends, test 2 waits past it.
// Without the shared afterEach, test 1's timer fires during test 2 and calls the mock.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

beforeEach(() => {
  apiMock.mockReset().mockResolvedValue({});
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});

describe("a pending ui-prefs save does not leak past its test", () => {
  it("schedules a save and ends without waiting for it", async () => {
    const s = await import("./settings.ts");
    expect(await s.hydrateUIPrefs()).toBe(true);
    s.setSetting("iconSet", "seti");
    expect(apiJSONMock).not.toHaveBeenCalled(); // still inside the debounce
  });

  it("sees no save from the previous test", async () => {
    await new Promise((r) => setTimeout(r, 900));
    expect(apiJSONMock).not.toHaveBeenCalled();
  });
});

describe("cancelPrefsTimersForTest", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("cancels the save and the hydrate retry and reports what was pending", async () => {
    vi.resetModules();
    const s = await import("./settings.ts");
    apiMock.mockResolvedValue({ error: { code: "http_502", message: "down" } });
    expect(await s.hydrateUIPrefs()).toBe(false);
    s.setSetting("iconSet", "material"); // not loaded: queues a change and arms the retry
    expect(s.cancelPrefsTimersForTest()).toEqual({ save: false, hydrateRetry: true });

    apiMock.mockResolvedValue({});
    await vi.advanceTimersByTimeAsync(70_000);
    expect(apiMock).toHaveBeenCalledTimes(1); // only the failed hydrate above
    expect(apiJSONMock).not.toHaveBeenCalled();
  });
});

describe("after the teardown cancel (#2977)", () => {
  it("refuses a timer armed late, and the next test can save again", async () => {
    vi.resetModules();
    const s = await import("./settings.ts");
    expect(await s.hydrateUIPrefs()).toBe(true);
    s.cancelPrefsTimersForTest(); // what the shared afterEach / afterAll does
    s.setSetting("iconSet", "seti"); // an async tail of the finished test
    expect(s.cancelPrefsTimersForTest()).toEqual({ save: false, hydrateRetry: false });

    (await import("./settings/sync.ts")).resumePrefsTimersForTest(); // what the shared beforeEach does
    s.setSetting("iconSet", "material");
    expect(s.cancelPrefsTimersForTest()).toEqual({ save: true, hydrateRetry: false });
  });
});

describe("a retired instance (end of its file)", () => {
  it("is not resumed by a later file's beforeEach", async () => {
    vi.resetModules();
    const s = await import("./settings.ts");
    expect(await s.hydrateUIPrefs()).toBe(true);
    const g = globalThis as any;
    const cancels: Set<() => unknown> = g[Symbol.for("af.prefsTimerHooks")];
    const resumes: Set<() => unknown> = g[Symbol.for("af.prefsTimerResumeHooks")];
    const saved = [new Set(cancels), new Set(resumes)];
    try {
      cancels.forEach((c) => c()); // what the shared afterAll does...
      cancels.clear();
      resumes.clear();
      resumes.forEach((r) => r()); // ...so a later beforeEach reaches nothing
      s.setSetting("iconSet", "seti"); // a tail from the retired file
      expect(s.cancelPrefsTimersForTest()).toEqual({ save: false, hydrateRetry: false });
    } finally {
      saved[0].forEach((c) => cancels.add(c));
      saved[1].forEach((r) => resumes.add(r));
    }
  });
});

describe("a hydrate still in flight when its test ends", () => {
  let finish!: (v: unknown) => void;

  it("starts a GET that never answers inside the test", async () => {
    vi.resetModules();
    apiMock.mockReset().mockReturnValue(new Promise((r) => (finish = r)));
    const s = await import("./settings.ts");
    void s.hydrateUIPrefs();
    expect(apiMock).toHaveBeenCalledTimes(1);
  });

  it("does not re-arm the retry when the late answer fails", async () => {
    vi.useFakeTimers();
    try {
      apiMock.mockClear();
      finish({ error: { code: "http_502", message: "down" } });
      await vi.advanceTimersByTimeAsync(70_000);
      expect(apiMock).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
