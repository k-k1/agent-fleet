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
    expect(shuffleDue("off", 0, at)).toBe(false);
    expect(shuffleDue("reload", 0, at)).toBe(false);
  });
  it("hourly: due an hour after the last pick", () => {
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
async function freshTab(owner = "a|u1") {
  vi.resetModules();
  const s = await import("./settings.ts");
  const sh = await import("./appearanceShuffle.ts");
  let o = owner;
  s.setPrefsOwnerSource(() => o);
  return { s, sh, setOwner: (v: string) => (o = v) };
}

const colorsOf = (st: Settings) => SURFACE_TARGETS.map((t) => st[t.key]);
const storageEvent = (key: string, newValue: string | null) =>
  window.dispatchEvent(new StorageEvent("storage", { key, newValue }));

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

  it("a scope with no schedule yet starts its clock instead of repainting at once", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ appearanceShuffle: "daily", topbarColor: "teal" }));
    const a = await freshTab();
    const stop = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    expect(a.s.getSettings().topbarColor).toBe("teal");
    const seeded = JSON.parse(localStorage.getItem("af-appearance-shuffle:*")!);
    expect(seeded.colors).toEqual({});
    stop();
  });

  it("two tabs that pick at once settle on the later pick", async () => {
    const a = await freshTab();
    const stopA = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    a.sh.shuffleAppearanceNow(2000);
    const own = colorsOf(a.s.getSettings());
    // The other tab's pick from just before ours arrives late: ignored.
    storageEvent("af-appearance-shuffle:*", JSON.stringify({ at: 1000, colors: { topbarColor: "mono" } }));
    expect(colorsOf(a.s.getSettings())).toEqual(own);
    // A later one is adopted.
    storageEvent("af-appearance-shuffle:*", JSON.stringify({ at: 3000, colors: { topbarColor: "mono" } }));
    expect(a.s.getSettings().topbarColor).toBe("mono");
    stopA();
  });

  it("adopting leaves the shared copy alone and ignores another scope's stamp", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ topbarColor: "teal" }));
    const a = await freshTab();
    const stop = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    storageEvent("af-appearance-shuffle:b|u1", JSON.stringify({ at: 5, colors: { topbarColor: "rose" } }));
    expect(a.s.getSettings().topbarColor).toBe("teal");
    storageEvent("af-appearance-shuffle:*", JSON.stringify({ at: 5, colors: { topbarColor: "rose" } }));
    expect(a.s.getSettings().topbarColor).toBe("rose");
    expect(JSON.parse(localStorage.getItem("af-display-settings")!).topbarColor).toBe("teal");
    stop();
  });

  it("follows the schedule when another tab changes it", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ appearanceShuffle: "1h" }));
    const a = await freshTab();
    const stop = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    storageEvent("af-display-settings", JSON.stringify({ appearanceShuffle: "off" }));
    expect(a.s.getSettings().appearanceShuffle).toBe("off");
    storageEvent("af-display-settings", JSON.stringify({ appearanceShuffle: "bogus" }));
    expect(a.s.getSettings().appearanceShuffle).toBe("off");
    stop();
  });

  it("an on-open pick waits for an unknown tenant owner instead of being dropped", async () => {
    localStorage.setItem(
      "af-display-settings",
      JSON.stringify({ appearanceShuffle: "reload", appearancePerTenant: true, topbarColor: "teal" }),
    );
    const a = await freshTab("");
    const stop = a.sh.startAppearanceShuffle();
    expect(a.s.getSettings().topbarColor).toBe("teal");
    a.setOwner("a|u1");
    vi.advanceTimersByTime(61_000);
    expect(a.s.getSettings().topbarColor).not.toBe("teal");
    const once = a.s.getSettings().topbarColor;
    vi.advanceTimersByTime(10 * 61_000);
    expect(a.s.getSettings().topbarColor).toBe(once);
    stop();
  });

  it("a pop-out window does not pick on open", async () => {
    localStorage.setItem("af-display-settings", JSON.stringify({ appearanceShuffle: "reload", topbarColor: "teal" }));
    const a = await freshTab();
    const stop = a.sh.startAppearanceShuffle({ pickOnLoad: false });
    vi.advanceTimersByTime(61_000);
    expect(a.s.getSettings().topbarColor).toBe("teal");
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
