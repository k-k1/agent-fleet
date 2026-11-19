import { describe, expect, it } from "vitest";
import { SURFACE_COLORS } from "./options.ts";
import { ON_ACCENT_DARK, ON_ACCENT_LIGHT, contrastRatio, onAccentColor, relativeLuminance } from "./onAccent.ts";

const L = ON_ACCENT_LIGHT;
const D = ON_ACCENT_DARK;

describe("onAccentColor", () => {
  it.each([
    ["#ffffff", D],
    ["#000000", L],
    ["#1d4ed8", L], // default light-theme blue
    ["#6cf", D], // short hex, default dark-theme cyan
    ["#2fb872", D], // green
    ["#e0964a", D], // warm
    ["#2fb3bd", D], // teal
    ["#3b82f6", D], // blue: only the darker near-black clears 4.5:1
    ["#e05770", D], // rose
    ["#7b7ff0", D], // indigo
    ["#9a9a9a", D], // mono
    // Around the crossover (both candidates ≈ 4.7:1 here, luminance ≈ 0.18).
    ["#2e8b57", D],
    ["#2563eb", L],
    ["#0f9d58", D],
    ["#0b8a4b", D], // luminance 0.188: dark 4.56 vs white 4.41
    ["#15803d", L], // luminance 0.159: white 5.02 vs dark 4.02
    ["#B8860B", D],
    ["#8a6d00", L],
  ])("%s → %s", (fill, want) => {
    expect(onAccentColor(fill)).toBe(want);
  });

  it("returns null for input it cannot read, so the CSS falls back", () => {
    for (const bad of ["", null, undefined, "transparent", "hsl(150 60% 45%)", "rgb(1,2,3)", "#12", "#12345", "#gggggg", "green"]) {
      expect(onAccentColor(bad as string | null | undefined)).toBeNull();
    }
  });

  it("is case- and whitespace-insensitive and expands short hex", () => {
    expect(onAccentColor(" #2FB872 ")).toBe(D);
    expect(relativeLuminance("#fff")).toBeCloseTo(1, 5);
    expect(relativeLuminance("#000")).toBeCloseTo(0, 5);
    expect(contrastRatio("#fff", "#000")).toBeCloseTo(21, 5);
  });

  it("reaches 4.5:1 for every accent in the palette", () => {
    for (const c of SURFACE_COLORS) {
      if (!c.accent) continue;
      const on = onAccentColor(c.accent)!;
      expect(contrastRatio(c.accent, on), c.id).toBeGreaterThanOrEqual(4.5);
    }
  });
});
