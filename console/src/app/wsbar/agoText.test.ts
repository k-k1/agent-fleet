// agoText must never print fractional seconds (Home adds a fractional client-side delta to the
// Agent's integer age) and must not render a rounded 60 s / 60 min as seconds / minutes.
import { describe, it, expect, vi } from "vitest";

// usageCommon.tsx imports the API client, which reads localStorage at load.
vi.mock("../../core/api/client.ts", () => ({ apiJSON: vi.fn() }));
import { agoText } from "./usageCommon.tsx";

describe("agoText", () => {
  it("rounds fractional seconds", () => {
    expect(agoText(35.49)).toBe("35 秒前");
    expect(agoText(35.489999999999995)).toBe("35 秒前");
    expect(agoText(0.2)).toBe("0 秒前");
  });
  it("promotes a value that rounds to 60 s to one minute", () => {
    expect(agoText(59.4)).toBe("59 秒前");
    expect(agoText(59.6)).toBe("1 分前");
  });
  it("promotes a value that rounds to 60 min to one hour", () => {
    expect(agoText(3569)).toBe("59 分前");
    expect(agoText(3599.6)).toBe("1 時間前");
  });
  it("does not round twice at the half-unit boundaries", () => {
    expect(agoText(89.6)).toBe("1 分前");
    expect(agoText(90)).toBe("2 分前");
    expect(agoText(5399.6)).toBe("1 時間前");
    expect(agoText(5400)).toBe("2 時間前");
  });
  it("returns empty for NaN, undefined and Infinity", () => {
    expect(agoText(NaN)).toBe("");
    expect(agoText(undefined)).toBe("");
    expect(agoText(Infinity)).toBe("");
  });
  it("clamps negatives and handles null", () => {
    expect(agoText(-3.2)).toBe("0 秒前");
    expect(agoText(null)).toBe("");
  });
});
