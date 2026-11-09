// Appearance changes made in another tab of the same browser (watchOtherTabs). Each "tab" is a
// fresh module instance; they share only localStorage, and the other tab's write is simulated as
// the browser reports it: the key written, then a storage event with its old and new value.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../core/api/client.ts", () => ({ api, apiJSON }));

const KEY = "af-display-settings";
const SNAP = "af-appearance-tenant:";

async function freshTab(owner = "a|u1") {
  vi.resetModules();
  const s = await import("./settings.ts");
  s.setPrefsOwnerSource(() => owner);
  s.applyTenantAppearance();
  const stop = s.watchOtherTabs();
  stops.push(stop);
  return s;
}
const stops: (() => void)[] = [];

// Another tab writes `key`: what was stored is the old value, `next` the new one.
function otherTabWrites(key: string, next: Record<string, unknown>) {
  const oldValue = localStorage.getItem(key);
  const newValue = JSON.stringify(next);
  localStorage.setItem(key, newValue);
  window.dispatchEvent(new StorageEvent("storage", { key, oldValue, newValue }));
}
const stored = (key = KEY) => JSON.parse(localStorage.getItem(key) || "{}");

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  api.mockReset().mockResolvedValue({});
  apiJSON.mockReset().mockResolvedValue({});
});

const allStored = () => Object.fromEntries(Object.keys(localStorage).map((k) => [k, localStorage.getItem(k)]));
afterEach(() => {
  while (stops.length) stops.pop()!();
  vi.useRealTimers();
});

describe("watchOtherTabs", () => {
  it("shows a color or theme changed in another tab, without writing anything itself", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal", theme: "dark" }));
    const s = await freshTab();
    // Loaded from the server, so a save could go out if adoption asked for one.
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(5_000);
    apiJSON.mockClear();
    const syncState = s.prefsSyncState();
    const before = allStored();
    otherTabWrites(KEY, { ...stored(), topbarColor: "rose", theme: "light" });
    expect(s.getSettings().topbarColor).toBe("rose");
    expect(s.getSettings().theme).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    await vi.advanceTimersByTimeAsync(5_000);
    // Only the other tab's own write changed storage.
    expect(allStored()).toEqual({ ...before, [KEY]: localStorage.getItem(KEY) });
    expect(JSON.parse(localStorage.getItem(KEY)!).topbarColor).toBe("rose");
    expect(apiJSON).not.toHaveBeenCalled();
    expect(s.prefsSyncState()).toBe(syncState);
  });

  it("takes only the keys the other tab's write changed, not the rest of its older copy", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal", viewerColor: "blue" }));
    const s = await freshTab();
    // This tab moved on (viewer -> green); the other tab still holds blue and changes the top bar.
    s.setSetting("viewerColor", "green");
    const theirs = { ...stored(), viewerColor: "blue" };
    localStorage.setItem(KEY, JSON.stringify(theirs));
    otherTabWrites(KEY, { ...theirs, topbarColor: "rose" });
    expect(s.getSettings().topbarColor).toBe("rose");
    expect(s.getSettings().viewerColor).toBe("green");
  });

  it("leaves non-appearance keys and malformed values alone", async () => {
    localStorage.setItem(KEY, JSON.stringify({ paneLayout: "tabs", termSize: 13, topbarColor: "teal" }));
    const s = await freshTab();
    otherTabWrites(KEY, { ...stored(), paneLayout: "split", termSize: 20, topbarColor: 7, appearanceShuffle: "bogus" });
    otherTabWrites(KEY, { ...stored(), topbarColor: "neon", theme: "sepia" });
    expect(s.getSettings().theme).toBe("dark");
    expect(s.getSettings().paneLayout).toBe("tabs");
    expect(s.getSettings().termSize).toBe(13);
    expect(s.getSettings().topbarColor).toBe("teal");
    expect(s.getSettings().appearanceShuffle).toBe("off");
    window.dispatchEvent(new StorageEvent("storage", { key: KEY, oldValue: null, newValue: "{not json" }));
    expect(s.getSettings().topbarColor).toBe("teal");
  });

  it("follows the random-colors schedule changed in another tab", async () => {
    localStorage.setItem(KEY, JSON.stringify({ appearanceShuffle: "1h" }));
    const s = await freshTab();
    otherTabWrites(KEY, { ...stored(), appearanceShuffle: "off" });
    expect(s.getSettings().appearanceShuffle).toBe("off");
  });

  it("ignores an event the stored value has already moved past", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
    const s = await freshTab();
    const oldValue = localStorage.getItem(KEY);
    const late = JSON.stringify({ topbarColor: "rose" });
    // A write from another tab whose event arrives after this tab wrote again.
    s.setSetting("topbarColor", "mono");
    window.dispatchEvent(new StorageEvent("storage", { key: KEY, oldValue, newValue: late }));
    expect(s.getSettings().topbarColor).toBe("mono");
  });

  it("a tab that writes before another tab's event arrives does not put that change back", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal", viewerColor: "blue" }));
    const a = await freshTab();
    const b = await freshTab();
    const l0 = localStorage.getItem(KEY);
    a.setSetting("topbarColor", "rose");
    const la = localStorage.getItem(KEY);
    // B changes another key before A's event reaches it.
    b.setSetting("viewerColor", "green");
    const lb = localStorage.getItem(KEY);
    expect(JSON.parse(lb!).topbarColor).toBe("rose");
    window.dispatchEvent(new StorageEvent("storage", { key: KEY, oldValue: l0, newValue: la }));
    window.dispatchEvent(new StorageEvent("storage", { key: KEY, oldValue: la, newValue: lb }));
    for (const t of [a, b]) {
      expect(t.getSettings().topbarColor).toBe("rose");
      expect(t.getSettings().viewerColor).toBe("green");
    }
  });

  it("takes what another tab stored before this tab started listening", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
    vi.resetModules();
    const s = await import("./settings.ts");
    s.setPrefsOwnerSource(() => "a|u1");
    // Written while no listener existed: no event will ever come for it.
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "rose" }));
    stops.push(s.watchOtherTabs());
    expect(s.getSettings().topbarColor).toBe("rose");
  });

  it("a server hydrate does not put back a change another tab made while it was waiting", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal", termSize: 13 }));
    const s = await freshTab();
    let answer!: (v: unknown) => void;
    api.mockReturnValueOnce(new Promise((r) => (answer = r)));
    const hydrating = s.hydrateUIPrefs();
    // Another tab changes the top bar; its event has not reached this tab yet.
    localStorage.setItem(KEY, JSON.stringify({ ...stored(), topbarColor: "rose" }));
    answer({ termSize: 20 });
    await hydrating;
    expect(s.getSettings().termSize).toBe(20);
    expect(s.getSettings().topbarColor).toBe("rose");
    expect(stored().topbarColor).toBe("rose");
  });

  describe("with per-tenant appearance", () => {
    it("turned on elsewhere: this tab switches to its own tenant's saved look", async () => {
      localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "indigo" }));
      const s = await freshTab("b|u1");
      // The other tab, on tenant a, turns the switch on showing a's look.
      otherTabWrites(KEY, { ...stored(), appearancePerTenant: true, topbarColor: "rose" });
      expect(s.getSettings().appearancePerTenant).toBe(true);
      expect(s.getSettings().topbarColor).toBe("indigo");
    });

    it("turned on elsewhere for a tenant with no saved look: the current look becomes its first", async () => {
      localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      otherTabWrites(KEY, { ...stored(), appearancePerTenant: true, topbarColor: "rose" });
      expect(s.getSettings().topbarColor).toBe("teal");
      expect(stored(SNAP + "b|u1").topbarColor).toBe("teal");
    });

    it("another tenant's look in the shared record is not taken; this tenant's snapshot is", async () => {
      localStorage.setItem(KEY, JSON.stringify({ appearancePerTenant: true, topbarColor: "teal" }));
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      otherTabWrites(SNAP + "a|u1", { topbarColor: "rose" });
      otherTabWrites(KEY, { ...stored(), topbarColor: "rose" });
      expect(s.getSettings().topbarColor).toBe("teal");
      otherTabWrites(SNAP + "b|u1", { topbarColor: "mono", theme: "light" });
      expect(s.getSettings().topbarColor).toBe("mono");
      expect(s.getSettings().theme).toBe("light");
    });

    it("the schedule still crosses tabs; the shared record's colors do not", async () => {
      localStorage.setItem(KEY, JSON.stringify({ appearancePerTenant: true, topbarColor: "teal" }));
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      otherTabWrites(KEY, { ...stored(), appearanceShuffle: "daily", topbarColor: "rose" });
      expect(s.getSettings().appearanceShuffle).toBe("daily");
      expect(s.getSettings().topbarColor).toBe("teal");
    });

    it("ignores a malformed or superseded snapshot event", async () => {
      localStorage.setItem(KEY, JSON.stringify({ appearancePerTenant: true, topbarColor: "teal" }));
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      otherTabWrites(SNAP + "b|u1", { topbarColor: "neon" });
      localStorage.setItem(SNAP + "b|u1", "{not json");
      window.dispatchEvent(new StorageEvent("storage", { key: SNAP + "b|u1", oldValue: null, newValue: "{not json" }));
      expect(s.getSettings().topbarColor).toBe("teal");
      // An event whose value has been overwritten since.
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "mono" }));
      window.dispatchEvent(
        new StorageEvent("storage", { key: SNAP + "b|u1", oldValue: null, newValue: JSON.stringify({ topbarColor: "rose" }) }),
      );
      expect(s.getSettings().topbarColor).toBe("teal");
    });

    it("a snapshot event is ignored while this tab shares one look", async () => {
      localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      otherTabWrites(SNAP + "b|u1", { topbarColor: "rose" });
      expect(s.getSettings().topbarColor).toBe("teal");
    });

    it("with the owner unknown, the switch is taken but nothing is filed and no snapshot is read", async () => {
      localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("");
      otherTabWrites(KEY, { ...stored(), appearancePerTenant: true, topbarColor: "rose" });
      expect(s.getSettings().appearancePerTenant).toBe(true);
      expect(s.getSettings().topbarColor).toBe("teal");
      expect(Object.keys(localStorage).filter((k) => k.startsWith(SNAP))).toEqual([]);
      otherTabWrites(SNAP + "", { topbarColor: "mono" });
      expect(s.getSettings().topbarColor).toBe("teal");
    });

    it("a tab of the same tenant that writes before the snapshot event arrives keeps the other's change", async () => {
      localStorage.setItem(KEY, JSON.stringify({ appearancePerTenant: true, topbarColor: "teal", viewerColor: "blue" }));
      localStorage.setItem(SNAP + "a|u1", JSON.stringify({ topbarColor: "teal", viewerColor: "blue" }));
      const a = await freshTab("a|u1");
      const b = await freshTab("a|u1");
      a.setSetting("topbarColor", "rose");
      // B changes another key before A's events reach it.
      b.setSetting("viewerColor", "green");
      expect(stored(SNAP + "a|u1")).toMatchObject({ topbarColor: "rose", viewerColor: "green" });
      expect(b.getSettings().topbarColor).toBe("rose");
    });

    it("turned off elsewhere: the look that tab showed becomes this tab's too", async () => {
      localStorage.setItem(KEY, JSON.stringify({ appearancePerTenant: true, topbarColor: "teal" }));
      localStorage.setItem(SNAP + "b|u1", JSON.stringify({ topbarColor: "teal" }));
      const s = await freshTab("b|u1");
      localStorage.setItem(KEY, JSON.stringify({ ...stored(), topbarColor: "rose" }));
      otherTabWrites(KEY, { ...stored(), appearancePerTenant: false });
      expect(s.getSettings().appearancePerTenant).toBe(false);
      expect(s.getSettings().topbarColor).toBe("rose");
      // And a later change of the shared record is followed again.
      otherTabWrites(KEY, { ...stored(), topbarColor: "mono" });
      expect(s.getSettings().topbarColor).toBe("mono");
    });
  });
});
