import { describe, expect, it } from "vitest";
import { prChecks, prStateKey, rowPorts, safePRURL } from "./sessionLinks.ts";

describe("sessionLinks", () => {
  it("names a draft apart from an open PR", () => {
    expect(prStateKey({ number: 1, state: "open", url: "" })).toBe("open");
    expect(prStateKey({ number: 1, state: "open", draft: true, url: "" })).toBe("draft");
    expect(prStateKey({ number: 1, state: "merged", draft: true, url: "" })).toBe("merged");
    expect(prStateKey({ number: 1, state: "closed", url: "" })).toBe("closed");
  });

  it("shows CI on open PRs only, and never invents green", () => {
    expect(prChecks({ number: 1, state: "open", url: "", checks: "pending" })).toBe("pending");
    expect(prChecks({ number: 1, state: "open", url: "" })).toBeNull();
    expect(prChecks({ number: 1, state: "closed", url: "", checks: "failure" })).toBeNull();
  });

  it("accepts github.com pages only", () => {
    expect(safePRURL("https://github.com/o/r/pull/1")).toBe("https://github.com/o/r/pull/1");
    expect(safePRURL("http://github.com/o/r/pull/1")).toBeNull();
    expect(safePRURL("https://github.com.evil.example/o/r/pull/1")).toBeNull();
    expect(safePRURL("javascript:alert(1)")).toBeNull();
    expect(safePRURL("")).toBeNull();
  });

  it("drops ports the browser pane would refuse", () => {
    expect(rowPorts([3000, 7700, 0, 70000, 8080])).toEqual([3000, 8080]);
    expect(rowPorts(undefined)).toEqual([]);
  });
});
