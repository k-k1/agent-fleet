import { beforeEach, describe, expect, it, vi } from "vitest";

// A null in the stored localStorage copy reads as a missing key, the same rule hydrateUIPrefs
// applies to a server null: load() must fall back to the default, never keep the null.
const KEY = "af-display-settings";

async function loadWith(stored: Record<string, unknown>) {
  localStorage.clear();
  localStorage.setItem(KEY, JSON.stringify(stored));
  vi.resetModules();
  const settings = await import("./settings.ts");
  const store = await import("./settings/store.ts");
  return { ...settings, persist: store.persist };
}

// Each case re-imports the whole settings module graph, which is slow under jsdom.
vi.setConfig({ testTimeout: 30_000 });

describe("load() and a stored null", () => {
  beforeEach(() => localStorage.clear());

  it("falls back to the default for every key stored as null", async () => {
    const base = await loadWith({});
    const defaults = base.getSettings() as unknown as Record<string, unknown>;
    const nulls = Object.fromEntries(Object.keys(defaults).map((k) => [k, null]));
    const s = await loadWith(nulls);
    const got = s.getSettings() as unknown as Record<string, unknown>;
    expect(Object.keys(got).filter((k) => got[k] === null)).toEqual([]);
    expect(got).toEqual(defaults);
  });

  it("keeps the non-null neighbours of a null key", async () => {
    const s = await loadWith({ quickReplies: null, theme: "dark" });
    const got = s.getSettings();
    expect(got.theme).toBe("dark");
    expect(got.quickReplies).not.toBeNull();
  });

  it("does not send a stored null on the next save", async () => {
    const s = await loadWith({ workingSets: null });
    s.persist();
    expect(JSON.parse(localStorage.getItem(KEY) || "{}").workingSets).not.toBeNull();
  });
});
