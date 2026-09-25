import { describe, expect, it } from "vitest";
import { contrastRatio, derivedLabelHex, labelBadgeColors, normalizeHex, readableForeground } from "./labelColor.ts";

describe("normalizeHex", () => {
  it("accepts GitHub's rrggbb with or without #, lowercased", () => {
    expect(normalizeHex("D73A4A")).toBe("d73a4a");
    expect(normalizeHex("#0e8a16")).toBe("0e8a16");
  });
  it("refuses anything that is not six hex digits (it lands in an inline style)", () => {
    for (const v of ["", "fff", "red", "url(x)", "d73a4a;color:red", "1234567", null, 42, {}]) {
      expect(normalizeHex(v)).toBe("");
    }
  });
});

describe("readableForeground", () => {
  it("puts black on light fills and white on dark ones", () => {
    expect(readableForeground("ededed")).toBe("000000"); // GitHub's default grey
    expect(readableForeground("a2eeef")).toBe("000000"); // enhancement
    expect(readableForeground("5319e7")).toBe("ffffff");
    expect(readableForeground("000000")).toBe("ffffff");
    // GitHub's own badge puts white on its bug red, at 4.2:1. Black is 5.0:1, so black it is.
    expect(readableForeground("d73a4a")).toBe("000000");
  });
  it("clears 4.5:1 on every fill, including the hardest mid-greys", () => {
    for (let v = 0; v < 256; v += 5) {
      const g = v.toString(16).padStart(2, "0").repeat(3);
      expect(contrastRatio(g, readableForeground(g))).toBeGreaterThanOrEqual(4.5);
    }
    for (const hex of ["d73a4a", "0075ca", "cfd3d7", "a2eeef", "7057ff", "008672", "e4e669", "d876e3", "fbca04"]) {
      expect(contrastRatio(hex, readableForeground(hex))).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe("labelBadgeColors", () => {
  it("uses the tracker's colour when it has a valid one", () => {
    expect(labelBadgeColors("wontfix", "5319e7")).toEqual({ bg: "#5319e7", fg: "#ffffff" });
  });
  it("derives a stable colour from the name otherwise (Jira, rows cached before colours)", () => {
    const a = labelBadgeColors("checkout");
    expect(labelBadgeColors("checkout", "not-a-colour")).toEqual(a);
    expect(labelBadgeColors("checkout")).toEqual(a);
    expect(a.bg).toMatch(/^#[0-9a-f]{6}$/);
    expect(derivedLabelHex("checkout")).not.toBe(derivedLabelHex("frontend"));
    expect(contrastRatio(a.bg.slice(1), a.fg.slice(1))).toBeGreaterThanOrEqual(4.5);
  });
});
