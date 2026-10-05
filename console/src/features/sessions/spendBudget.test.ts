import { describe, expect, it } from "vitest";
import { fmtSpend, parseCap, spendLevel, suggestedRaise } from "./spendBudget.ts";

describe("spend budget helpers", () => {
  it("always writes the spend as an approximation", () => {
    expect(fmtSpend(1.8449)).toBe("≈$1.84");
    expect(fmtSpend(0)).toBe("≈$0.00");
    expect(fmtSpend(Number.NaN)).toBe("≈$0.00");
  });

  it("accepts dollars and cents only", () => {
    expect(parseCap("5")).toBe(5);
    expect(parseCap("$7.25")).toBe(7.25);
    expect(parseCap("")).toBe(0);
    expect(parseCap("-1")).toBeNaN();
    expect(parseCap("1.234")).toBeNaN();
    expect(parseCap("1e9")).toBeNaN();
    expect(parseCap("200000")).toBeNaN();
  });

  it("turns amber at 80% and red at the cap", () => {
    expect(spendLevel(10, undefined)).toBe("none");
    expect(spendLevel(3.99, 5)).toBe("ok");
    expect(spendLevel(4, 5)).toBe("warn");
    expect(spendLevel(5, 5)).toBe("over");
  });

  it("suggests a raise that clears the spend", () => {
    expect(suggestedRaise(5, 5.2)).toBe(8);
    expect(suggestedRaise(5, 9)).toBe(12); // a hard stop at 2× the cap: 9 × 1.25 = 11.25
    expect(suggestedRaise(0.5, 0.5)).toBe(2);
  });
});
