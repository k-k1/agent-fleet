import { describe, expect, it } from "vitest";
import { jdkLabel, jdkVendor } from "./jdkLabel.ts";

describe("jdkLabel", () => {
  it("reads a bare major as Temurin and a prefixed id as its vendor", () => {
    expect(jdkLabel("21")).toBe("Temurin 21");
    expect(jdkLabel("corretto-8")).toBe("Corretto 8");
    expect(jdkVendor("21")).toBe("Temurin");
    expect(jdkVendor("corretto-21")).toBe("Corretto");
  });
  it("shows anything else as is, the way the agent's Label does", () => {
    expect(jdkLabel("")).toBe("");
    expect(jdkLabel("system")).toBe("system");
    expect(jdkLabel("temurin-21")).toBe("temurin-21");
    expect(jdkLabel("zulu-21")).toBe("zulu-21");
    expect(jdkVendor("system")).toBe("");
    expect(jdkVendor("")).toBe("");
  });
});
