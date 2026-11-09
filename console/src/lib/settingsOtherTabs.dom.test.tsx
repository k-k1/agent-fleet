// Appearance changes made in another tab of the same browser (watchOtherTabs). Each "tab" is a
// fresh module instance; they share only localStorage, and the other tab's write is simulated as
// the browser reports it: the key written, then a storage event with its old and new value.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../core/api/client.ts", () => ({
  api: vi.fn().mockResolvedValue({}),
  apiJSON: vi.fn().mockResolvedValue({}),
}));

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
});
afterEach(() => {
  while (stops.length) stops.pop()!();
  vi.useRealTimers();
});

describe("watchOtherTabs", () => {
  it("shows a color or theme changed in another tab, without writing anything itself", async () => {
    localStorage.setItem(KEY, JSON.stringify({ topbarColor: "teal", theme: "dark" }));
    const s = await freshTab();
    otherTabWrites(KEY, { ...stored(), topbarColor: "rose", theme: "light" });
    expect(s.getSettings().topbarColor).toBe("rose");
    expect(s.getSettings().theme).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
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
