import { describe, expect, it } from "vitest";
import { browserTarget, targetFromURL } from "./target.ts";

describe("browserTarget", () => {
  it("accepts only a valid container port and root-relative path", () => {
    expect(browserTarget(3000, "/users/1?q=x#detail")).toEqual({ port: 3000, path: "/users/1?q=x#detail" });
    expect(browserTarget(0, "/")).toBeNull();
    expect(browserTarget(7700, "/")).toBeNull();
    expect(browserTarget(65536, "/")).toBeNull();
    expect(browserTarget(3000, "users")).toBeNull();
    expect(browserTarget(3000, "//example.com/")).toBeNull();
  });

  it("extracts a restorable target only from loopback navigation", () => {
    expect(targetFromURL("http://127.0.0.1:5173/app?q=1#x")).toEqual({ port: 5173, path: "/app?q=1#x" });
    expect(targetFromURL("https://example.com/app")).toBeNull();
  });
});
