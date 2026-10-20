// The memoryReview setting (ADR 0117) is the key the Agent reads (uiprefs.MemoryReview): "propose"
// or off, and everything else off, so the Console never shows a mode the Agent would not enforce.
import { describe, expect, it } from "vitest";
import { normalizeMemoryReview, settingsDefaults } from "./settings.ts";

describe("memoryReview setting", () => {
  it("is off by default", () => {
    expect(settingsDefaults().memoryReview).toBe("off");
  });

  it("reads like the Agent: only the string propose is on", () => {
    expect(normalizeMemoryReview("propose")).toBe("propose");
    for (const v of ["off", "auto", "PROPOSE", true, 1, null, undefined, {}]) {
      expect(normalizeMemoryReview(v)).toBe("off");
    }
  });
});
