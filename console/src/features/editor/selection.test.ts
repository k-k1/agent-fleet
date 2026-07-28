import { describe, expect, it } from "vitest";
import { EditorState } from "@codemirror/state";
import { lineStartOf, selectionRangeOf } from "./selection.ts";

const stateOf = (doc: string, anchor?: number, head?: number) =>
  EditorState.create({
    doc,
    selection: anchor === undefined ? undefined : { anchor, head: head ?? anchor },
  });

describe("selectionRangeOf", () => {
  it("returns null without a quotable selection", () => {
    expect(selectionRangeOf(stateOf("alpha\nbeta"))).toBeNull();
    expect(selectionRangeOf(stateOf("alpha\nbeta", 2, 2))).toBeNull();
    // Whitespace-only, like the read-only view's `quote.trim()` guard.
    expect(selectionRangeOf(stateOf("alpha\n   \nbeta", 6, 9))).toBeNull();
  });

  it("reports 1-based inclusive line numbers", () => {
    const state = stateOf("alpha\nbeta\ngamma", 2, 12);
    expect(selectionRangeOf(state)).toEqual({
      quote: "pha\nbeta\ng",
      startLine: 1,
      endLine: 3,
      from: 2,
    });
  });

  it("normalises a backwards selection", () => {
    // `main.from`/`.to` are ordered, so dragging upwards reports the same range.
    expect(selectionRangeOf(stateOf("alpha\nbeta", 9, 1))).toEqual(
      selectionRangeOf(stateOf("alpha\nbeta", 1, 9)),
    );
  });

  it("slices the whole quote from the document, not the rendered rows", () => {
    // The regression this guards: CodeMirror only renders rows near the
    // viewport, so reading the quote from the DOM truncates a long selection.
    // Slicing the document keeps every line, however far it runs.
    const doc = Array.from({ length: 5000 }, (_, i) => `line ${i + 1}`).join("\n");
    const state = stateOf(doc, 0, doc.length);
    const range = selectionRangeOf(state)!;
    expect(range.startLine).toBe(1);
    expect(range.endLine).toBe(5000);
    expect(range.quote).toBe(doc);
    expect(range.quote.split("\n")).toHaveLength(5000);
  });
});

describe("lineStartOf", () => {
  it("returns the offset of a 1-based line", () => {
    const state = stateOf("alpha\nbeta\ngamma");
    expect(lineStartOf(state, 1)).toBe(0);
    expect(lineStartOf(state, 2)).toBe(6);
    expect(lineStartOf(state, 3)).toBe(11);
  });

  it("clamps a citation that falls outside the document", () => {
    const state = stateOf("alpha\nbeta");
    expect(lineStartOf(state, 0)).toBe(0);
    expect(lineStartOf(state, -3)).toBe(0);
    expect(lineStartOf(state, 99)).toBe(6);
    expect(lineStartOf(state, 2.7)).toBe(6);
  });

  it("handles an empty document", () => {
    expect(lineStartOf(stateOf(""), 5)).toBe(0);
  });
});
