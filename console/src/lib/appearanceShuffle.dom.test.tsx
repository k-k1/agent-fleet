import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { pickColors, shuffleDue } from "./appearanceShuffle.ts";
import { SURFACE_TARGETS, settingsDefaults, type Settings } from "./settings.ts";

// The settings module reaches the API client on import; the shuffle never talks to the server.
vi.mock("../core/api/client.ts", () => ({
  api: vi.fn().mockResolvedValue({}),
  apiJSON: vi.fn().mockResolvedValue({}),
}));

// A deterministic stand-in for Math.random walking through [0, 1).
function seq(...xs: number[]): () => number {
  let i = 0;
  return () => xs[i++ % xs.length];
}

describe("pickColors", () => {
  it("gives every surface a distinct, non-default color that differs from its current one", () => {
    const current: Settings = {
      ...settingsDefaults(),
      topbarColor: "slate",
      leftpaneColor: "blue",
      viewerColor: "green",
      chatColor: "purple",
      sharedColor: "warm",
      assistantColor: "teal",
    };
    for (const r of [0, 0.5, 0.999]) {
      for (let n = 0; n < 50; n++) {
        const rand = n === 0 ? seq(r) : Math.random;
        const out = pickColors(current, rand) as Record<string, string>;
        const ids = SURFACE_TARGETS.map((t) => out[t.key]);
        expect(ids.every(Boolean)).toBe(true);
        expect(new Set(ids).size).toBe(ids.length);
        expect(ids).not.toContain("default");
        for (const t of SURFACE_TARGETS) expect(out[t.key]).not.toBe(current[t.key]);
      }
    }
  });
});

describe("shuffleDue", () => {
  const at = new Date(2026, 10, 9, 10, 0).getTime();
  it("never fires on a check when off or once per load", () => {
    expect(shuffleDue("off", null, at)).toBe(false);
    expect(shuffleDue("reload", null, at)).toBe(false);
  });
  it("hourly: due an hour after the last pick", () => {
    expect(shuffleDue("1h", null, at)).toBe(true);
    expect(shuffleDue("1h", at, at + 59 * 60_000)).toBe(false);
    expect(shuffleDue("1h", at, at + 60 * 60_000)).toBe(true);
  });
  it("daily: due on a new local day, not 24 hours later", () => {
    expect(shuffleDue("daily", at, new Date(2026, 10, 9, 23, 59).getTime())).toBe(false);
    expect(shuffleDue("daily", at, new Date(2026, 10, 10, 0, 1).getTime())).toBe(true);
  });
  it("a stamp from the future (clock moved back) counts as due", () => {
    expect(shuffleDue("1h", at + HOUR, at)).toBe(true);
    expect(shuffleDue("daily", at + HOUR, at)).toBe(true);
  });
});
const HOUR = 60 * 60_000;

// One module instance per "tab": the stamp key in the shared localStorage is the only channel.
async function freshTab() {
  vi.resetModules();
  const s = await import("./settings.ts");
  const sh = await import("./appearanceShuffle.ts");
  s.setPrefsOwnerSource(() => "a|u1");
  return { s, sh };
}

describe("schedule across tabs", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
  });
  afterEach(() => vi.useRealTimers());

  it("switching on picks at once without touching the theme, and another tab adopts the pick", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ theme: "light" }));
    const b = await freshTab();
    const stopB = b.sh.startAppearanceShuffle();
    const a = await freshTab();
    a.sh.setAppearanceShuffle("1h");
    const picked = a.s.getSettings();
    expect(picked.theme).toBe("light");
    for (const t of SURFACE_TARGETS) expect(picked[t.key]).not.toBe("default");

    const key = "af-appearance-shuffle:*";
    window.dispatchEvent(new StorageEvent("storage", { key, newValue: localStorage.getItem(key) }));
    for (const t of SURFACE_TARGETS) expect(b.s.getSettings()[t.key]).toBe(picked[t.key]);
    stopB();
  });

  it("an hourly schedule re-picks once the hour is up, and only then", async () => {
    vi.setSystemTime(new Date(2026, 10, 9, 10, 0));
    const a = await freshTab();
    a.sh.setAppearanceShuffle("1h");
    const first = a.s.getSettings().topbarColor;
    const stop = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    vi.advanceTimersByTime(59 * 60_000);
    expect(a.s.getSettings().topbarColor).toBe(first);
    vi.advanceTimersByTime(2 * 60_000);
    expect(a.s.getSettings().topbarColor).not.toBe(first);
    stop();
  });

  it("with per-tenant appearance on, the schedule is kept per tenant", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ appearancePerTenant: true }));
    const a = await freshTab();
    a.sh.shuffleAppearanceNow();
    expect(localStorage.getItem("af-appearance-shuffle:a|u1")).not.toBeNull();
    expect(localStorage.getItem("af-appearance-shuffle:*")).toBeNull();
  });
});
