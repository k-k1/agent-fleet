import { describe, expect, it } from "vitest";
import { fmtContext, fmtUSD } from "./modelInfoFormat.ts";

describe("fmtUSD", () => {
  it("keeps the digits that tell models apart", () => {
    expect(fmtUSD(5)).toBe("$5");
    expect(fmtUSD(30)).toBe("$30");
    expect(fmtUSD(0.1)).toBe("$0.10");
    expect(fmtUSD(2.5)).toBe("$2.50");
    expect(fmtUSD(1.25)).toBe("$1.25");
    expect(fmtUSD(0.075)).toBe("$0.075");
    expect(fmtUSD(0.01)).toBe("$0.01");
    expect(fmtUSD(0.005)).toBe("$0.005");
    expect(fmtUSD(0)).toBe("$0");
  });
});

describe("fmtContext", () => {
  it("writes windows as the vendors do", () => {
    expect(fmtContext(1_050_000)).toBe("1.05M");
    expect(fmtContext(1_048_576)).toBe("1.05M");
    expect(fmtContext(1_000_000)).toBe("1M");
    expect(fmtContext(204_800)).toBe("205K");
    expect(fmtContext(128_000)).toBe("128K");
    expect(fmtContext(512)).toBe("512");
  });
});
