import { describe, expect, it } from "vitest";
import { buildInfo, buildLabel, hasNewBuild } from "./version.ts";

// The comparison that decides whether to prompt for a reload. buildInfo.time is stamped
// at build time (vite define) — present under vitest, which shares the vite config.
describe("hasNewBuild", () => {
  it("returns false for a null server response (offline / 401 / missing file)", () => {
    expect(hasNewBuild(null)).toBe(false);
  });

  it("returns false when the server build matches the running build", () => {
    expect(hasNewBuild({ time: buildInfo.time, sha: buildInfo.sha })).toBe(false);
  });

  it("returns true when the server build id differs", () => {
    if (!buildInfo.time) return; // unstamped context can't detect updates by design
    expect(hasNewBuild({ time: buildInfo.time + "x", sha: "" })).toBe(true);
  });

  it("never flags an update when the running build is unstamped", () => {
    // Guarded by !!buildInfo.time — an unstamped dev build must not nag on every poll.
    if (buildInfo.time) return;
    expect(hasNewBuild({ time: "2099-01-01T00:00:00.000Z", sha: "z" })).toBe(false);
  });
});

describe("buildLabel", () => {
  it("is always a non-empty string", () => {
    expect(typeof buildLabel()).toBe("string");
    expect(buildLabel().length).toBeGreaterThan(0);
  });
});
