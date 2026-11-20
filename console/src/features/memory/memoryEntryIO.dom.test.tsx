import { describe, expect, it } from "vitest";
import { decodeEntry, encodeEntry } from "./memoryEntryIO.ts";

describe("memory entry buffer", () => {
  it("round-trips the fields, whatever the description holds", () => {
    const f = { description: 'a "quoted"\nmulti-line {x}', type: "project", body: "line 1\n\nline 3\n" };
    expect(decodeEntry(encodeEntry(f))).toEqual(f);
  });

  it("keeps the body readable after one header line", () => {
    expect(encodeEntry({ description: "d", type: "user", body: "text" })).toBe('{"description":"d","type":"user"}\ntext');
  });

  it("takes text that is not an encoded entry as a bare body", () => {
    expect(decodeEntry("just text\nmore")).toEqual({ description: "", type: "feedback", body: "just text\nmore" });
  });
});
