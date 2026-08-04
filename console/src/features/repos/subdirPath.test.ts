// normalizeSubdir keeps typed/pasted input in the slash-relative shape the wire wants.
// It deliberately does NOT judge ".." — the Agent (session.CleanSubdir) rejects escapes,
// and duplicating that rule here would let the two drift apart.
import { describe, expect, it } from "vitest";
import { normalizeSubdir } from "./subdirPath.ts";

describe("normalizeSubdir", () => {
  it("strips the decoration users type or paste", () => {
    expect(normalizeSubdir("  console  ")).toBe("console");
    expect(normalizeSubdir("./console/src")).toBe("console/src");
    expect(normalizeSubdir("/console/src/")).toBe("console/src");
    expect(normalizeSubdir("console\\src")).toBe("console/src");
    expect(normalizeSubdir("")).toBe("");
  });

  it("leaves an escape alone for the server to refuse", () => {
    expect(normalizeSubdir("../sibling")).toBe("../sibling");
  });
});
