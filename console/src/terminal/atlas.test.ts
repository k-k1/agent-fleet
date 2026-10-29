import { describe, it, expect, vi } from "vitest";
import type { Terminal } from "@xterm/xterm";
import { rebuildGlyphAtlas, type AtlasTerm } from "./atlas.ts";

// fake builds a pane the way term.ts holds one: a terminal (with the private render
// service rebuildGlyphAtlas reaches into) plus the renderer addon that is loaded.
function fake(renderer: "webgl" | "canvas" | "dom", atlas?: object) {
  const calls: string[] = [];
  const term = {
    rows: 24,
    clearTextureAtlas: vi.fn(() => calls.push("clearTextureAtlas")),
    refresh: vi.fn((s: number, e: number) => calls.push(`refresh ${s}-${e}`)),
    _core: { _renderService: { clear: vi.fn(() => calls.push("clear")) } },
  };
  const t: AtlasTerm = { term: term as unknown as Terminal };
  if (renderer === "webgl") t.webgl = { _renderer: { _charAtlas: atlas } };
  if (renderer === "canvas") t.canvas = {};
  return { t, term, calls };
}

describe("rebuildGlyphAtlas", () => {
  it("rebuilds the model of a WebGL sibling sharing the atlas, then repaints it in full", () => {
    const shared = {};
    const a = fake("webgl", shared);
    const b = fake("webgl", shared);
    rebuildGlyphAtlas(a.t, [a.t, b.t]);
    expect(a.calls).toEqual(["clearTextureAtlas"]);
    expect(b.calls).toEqual(["clear", "refresh 0-23"]);
  });

  it("leaves a WebGL sibling with a different atlas alone", () => {
    const a = fake("webgl", {});
    const b = fake("webgl", {});
    rebuildGlyphAtlas(a.t, [a.t, b.t]);
    expect(b.calls).toEqual([]);
  });

  it("treats an unreadable atlas as shared", () => {
    const a = fake("webgl", {});
    const b = fake("webgl", undefined);
    rebuildGlyphAtlas(a.t, [a.t, b.t]);
    expect(b.calls).toEqual(["clear", "refresh 0-23"]);
  });

  it("never clears a DOM or canvas sibling", () => {
    const shared = {};
    const a = fake("webgl", shared);
    const dom = fake("dom");
    const canvas = fake("canvas");
    rebuildGlyphAtlas(a.t, [a.t, dom.t, canvas.t]);
    expect(dom.calls).toEqual([]);
    expect(canvas.calls).toEqual([]);
  });

  it("clears a canvas pane's own atlas without touching WebGL panes", () => {
    const c = fake("canvas");
    const w = fake("webgl", {});
    rebuildGlyphAtlas(c.t, [c.t, w.t]);
    expect(c.calls).toEqual(["clearTextureAtlas"]);
    expect(w.calls).toEqual([]);
  });

  it("does nothing on the DOM renderer", () => {
    const d = fake("dom");
    const w = fake("webgl", {});
    rebuildGlyphAtlas(d.t, [d.t, w.t]);
    expect(d.calls).toEqual([]);
    expect(w.calls).toEqual([]);
  });
});
