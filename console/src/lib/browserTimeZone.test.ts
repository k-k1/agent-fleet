import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let stored = "";
const setSetting = vi.fn((_k: string, v: string) => {
  stored = v;
});
vi.mock("./settings.ts", () => ({
  getSettings: () => ({ browserTimeZone: stored }),
  setSetting: (k: string, v: string) => setSetting(k, v),
}));
vi.mock("./intl.ts", () => ({ isValidTimeZone: (n: unknown) => n === "Asia/Tokyo" || n === "America/New_York" }));

const { publishBrowserTimeZone, resolveBrowserTimeZone } = await import("./browserTimeZone.ts");

let browser: unknown = "Asia/Tokyo";
const realIntl = globalThis.Intl;

beforeEach(() => {
  stored = "";
  browser = "Asia/Tokyo";
  setSetting.mockClear();
  vi.stubGlobal("Intl", {
    ...realIntl,
    DateTimeFormat: function () {
      return { resolvedOptions: () => ({ timeZone: browser }) };
    },
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("publishBrowserTimeZone", () => {
  it("writes the resolved zone when nothing is stored", () => {
    publishBrowserTimeZone();
    expect(setSetting).toHaveBeenCalledExactlyOnceWith("browserTimeZone", "Asia/Tokyo");
  });

  it("does not write when the stored value already equals the browser's", () => {
    stored = "Asia/Tokyo";
    publishBrowserTimeZone();
    publishBrowserTimeZone();
    expect(setSetting).not.toHaveBeenCalled();
  });

  it("writes once per change, so a repeated call after a write is silent", () => {
    publishBrowserTimeZone();
    browser = "America/New_York";
    publishBrowserTimeZone();
    publishBrowserTimeZone();
    expect(setSetting.mock.calls).toEqual([
      ["browserTimeZone", "Asia/Tokyo"],
      ["browserTimeZone", "America/New_York"],
    ]);
  });

  it("publishes nothing for a zone Intl rejects, an empty one or a non-string", () => {
    for (const bad of ["Mars/Olympus", "", undefined, 7, "A".repeat(500)]) {
      browser = bad;
      expect(resolveBrowserTimeZone()).toBe("");
      publishBrowserTimeZone();
    }
    expect(setSetting).not.toHaveBeenCalled();
  });
});
