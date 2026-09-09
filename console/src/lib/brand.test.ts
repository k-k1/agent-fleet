import { afterEach, describe, expect, it, vi } from "vitest";

// brand.ts reads the injected globals ONCE, at import time (the CP puts them in the shell
// ahead of the bundle), so every case has to re-import the module.
const load = async (injected: unknown) => {
  vi.resetModules();
  const g = globalThis as { __AF_BRAND?: unknown };
  if (injected === undefined) delete g.__AF_BRAND;
  else g.__AF_BRAND = injected;
  return await import("./brand.ts");
};

afterEach(() => {
  delete (globalThis as { __AF_BRAND?: unknown }).__AF_BRAND;
});

describe("brand", () => {
  it("falls back to the shipped identity when the deployment brands nothing", async () => {
    const b = await load(undefined);
    expect(b.brandLabel).toBe("");
    expect(b.brandName).toBe("Agent Fleet");
    expect(b.appTitle).toBe("Agent Fleet — Console");
  });

  it("takes the deployment's label, name and colour", async () => {
    const b = await load({ label: "dev", name: "[dev] Agent Fleet", color: "#7c4dff" });
    expect(b.brandLabel).toBe("dev");
    expect(b.appTitle).toBe("[dev] Agent Fleet — Console");
    expect(b.brandColor).toBe("#7c4dff");
  });

  // A half-written injection must not produce "undefined — Console" in the tab strip.
  it("ignores empty members", async () => {
    const b = await load({ label: "  ", name: "" });
    expect(b.brandLabel).toBe("");
    expect(b.brandName).toBe("Agent Fleet");
    expect(b.brandColor).toBe("#149ba7");
  });
});

// The bar is 4.5:1 for 11px text, and NO single ink clears it across the palette: white
// is 3.35:1 on the shipped teal and 3.01:1 on orange, black is 4.42:1 on magenta. So the
// assertion is the contrast itself, not which of the two was picked.
const contrast = (a: string, b: string) => {
  const lum = (hex: string) => {
    const h = hex.replace("#", "");
    // "#fff" -> ffffff, so the ink shorthand measures like any other colour.
    const n = parseInt(h.length === 3 ? [...h].map((c) => c + c).join("") : h, 16);
    const chan = (c: number) => {
      const s = c / 255;
      return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * chan((n >> 16) & 255) + 0.7152 * chan((n >> 8) & 255) + 0.0722 * chan(n & 255);
  };
  const [x, y] = [lum(a) + 0.05, lum(b) + 0.05].sort((p, q) => q - p);
  return x / y;
};

describe("brandInk", () => {
  it.each(["#149ba7", "#2f6fed", "#7c4dff", "#c2447d", "#d94b4b", "#e07a1f", "#2f9e44", "#64748b"])(
    "stays readable on %s",
    async (color) => {
      const b = await load({ color });
      expect(contrast(color, b.brandInk)).toBeGreaterThanOrEqual(4.5);
    },
  );

  // A colour the CP never sends (or a mangled one) must still produce usable ink.
  it("falls back to white on an unparsable colour", async () => {
    expect((await load({ color: "not-a-colour" })).brandInk).toBe("#fff");
  });
});
