import { describe, expect, it } from "vitest";
import { derivedLabelHex, labelColor, normalizeHex } from "./labelColor.ts";

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

describe("labelColor", () => {
  it("uses the tracker's colour when it has a valid one", () => {
    expect(labelColor("bug", "D73A4A")).toBe("#d73a4a");
  });
  it("derives a stable colour from the name otherwise (Jira, rows cached before colours)", () => {
    const a = labelColor("checkout");
    expect(labelColor("checkout", "not-a-colour")).toBe(a);
    expect(labelColor("checkout")).toBe(a);
    expect(a).toMatch(/^#[0-9a-f]{6}$/);
    expect(derivedLabelHex("checkout")).not.toBe(derivedLabelHex("frontend"));
  });
});
