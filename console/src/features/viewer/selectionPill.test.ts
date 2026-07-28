import { describe, expect, it } from "vitest";
import { editorPill, type SelectionPill } from "./selectionPill.ts";
import type { EditorSelectionReport } from "../editor/selection.ts";

const report = (over: Partial<EditorSelectionReport> = {}): EditorSelectionReport => ({
  quote: "hello",
  startLine: 2,
  endLine: 2,
  from: 6,
  coords: { left: 120, top: 240 },
  reason: "selection",
  ...over,
});

const pill = (over: Partial<SelectionPill> = {}): SelectionPill => ({
  quote: "hello",
  startLine: 2,
  endLine: 2,
  x: 120,
  y: 206,
  origin: "editor",
  ...over,
});

const fromView = pill({ origin: "view", quote: "elsewhere", x: 10, y: 20 });

describe("editorPill", () => {
  it("takes the pill on a new selection", () => {
    expect(editorPill(null, report(), true)).toEqual(pill());
    expect(editorPill(fromView, report(), true)).toEqual(pill());
  });

  it("clears only its own pill", () => {
    expect(editorPill(pill(), null, true)).toBeNull();
    // A selection made on the other surface is not the editor's to clear.
    expect(editorPill(fromView, null, true)).toBe(fromView);
  });

  it("says nothing while the surface is hidden", () => {
    // CodeMirror keeps its selection and still reports layout changes behind a
    // hidden shell; none of that may put a pill back on screen.
    expect(editorPill(fromView, report(), false)).toBe(fromView);
    expect(editorPill(pill(), report(), false)).toBeNull();
    expect(editorPill(null, report(), false)).toBeNull();
  });

  it("re-measures its own pill but never reclaims another's", () => {
    // The regression: after selecting in the preview, a scroll or resize of the
    // editor re-reports its still-present selection. That must not flip the pill
    // back to the editing surface.
    expect(editorPill(fromView, report({ reason: "geometry" }), true)).toBe(fromView);
    expect(editorPill(null, report({ reason: "geometry" }), true)).toBeNull();
    // Its own pill does follow the text it is anchored to.
    expect(
      editorPill(pill(), report({ reason: "geometry", coords: { left: 40, top: 90 } }), true),
    ).toEqual(pill({ x: 40, y: 56 }));
  });

  it("drops the pill when the selection scrolls out of the rendered range", () => {
    expect(editorPill(pill(), report({ coords: null }), true)).toBeNull();
    expect(editorPill(fromView, report({ coords: null }), true)).toBe(fromView);
  });

  it("rounds coordinates for the fixed-position pill", () => {
    expect(editorPill(null, report({ coords: { left: 12.4, top: 99.6 } }), true)).toMatchObject({
      x: 12,
      y: 66,
    });
  });
});
