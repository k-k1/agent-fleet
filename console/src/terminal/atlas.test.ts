import { describe, it, expect, vi } from "vitest";
import type { Terminal } from "@xterm/xterm";
import { fontFamilies, rebuildAtlasesFor, rebuildGlyphAtlas, resetGlyphModel, type AtlasTerm } from "./atlas.ts";

// fake builds a pane the way term.ts holds one: a terminal (with the private render
// service rebuildGlyphAtlas reaches into) plus the renderer addon that is loaded.
function fake(renderer: "webgl" | "canvas" | "dom", atlas?: object, fontFamily = '"JetBrains Mono", monospace') {
  const calls: string[] = [];
  const term = {
    rows: 24,
    options: { fontFamily },
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

  it("skips placeholders without a terminal and keeps going past a sibling that throws", () => {
    const shared = {};
    const a = fake("webgl", shared);
    const broken = fake("webgl", shared);
    broken.term.refresh.mockImplementation(() => {
      throw new Error("boom");
    });
    const c = fake("webgl", shared);
    const placeholder: AtlasTerm = { webgl: {} };
    rebuildGlyphAtlas(a.t, [a.t, placeholder, broken.t, c.t]);
    expect(c.calls).toEqual(["clear", "refresh 0-23"]);
  });

  it("treats an atlas getter that throws as shared", () => {
    const a = fake("webgl", {});
    const b = fake("webgl");
    Object.defineProperty(b.t.webgl as object, "_renderer", {
      get() {
        throw new Error("moved");
      },
    });
    rebuildGlyphAtlas(a.t, [a.t, b.t]);
    expect(b.calls).toEqual(["clear", "refresh 0-23"]);
  });
});

describe("resetGlyphModel", () => {
  it("clears a WebGL pane's model without touching its atlas or repainting", () => {
    const w = fake("webgl", {});
    resetGlyphModel(w.t);
    expect(w.calls).toEqual(["clear"]);
  });

  it("never clears the DOM renderer or the canvas fallback", () => {
    const d = fake("dom");
    const c = fake("canvas");
    resetGlyphModel(d.t);
    resetGlyphModel(c.t);
    expect(d.calls).toEqual([]);
    expect(c.calls).toEqual([]);
  });

  it("survives a render service that is gone", () => {
    const w = fake("webgl", {});
    (w.term as { _core?: unknown })._core = undefined;
    expect(() => resetGlyphModel(w.t)).not.toThrow();
  });
});

describe("fontFamilies", () => {
  it("strips quotes and spaces and lower-cases each family", () => {
    expect(fontFamilies(`"JetBrains Mono", 'AF CJK Ambiguous', monospace`)).toEqual([
      "jetbrains mono",
      "af cjk ambiguous",
      "monospace",
    ]);
    expect(fontFamilies("")).toEqual([]);
  });
});

describe("rebuildAtlasesFor", () => {
  it("rebuilds a shared atlas once when its font arrives, siblings included", () => {
    const shared = {};
    const a = fake("webgl", shared);
    const b = fake("webgl", shared);
    rebuildAtlasesFor([a.t, b.t], new Set(["jetbrains mono"]));
    expect(a.calls).toEqual(["clearTextureAtlas"]);
    expect(b.calls).toEqual(["clear", "refresh 0-23"]);
  });

  it("leaves panes whose font list does not name the arrived family", () => {
    const a = fake("webgl", {}, "monospace");
    const c = fake("canvas", undefined, "monospace");
    rebuildAtlasesFor([a.t, c.t], new Set(["jetbrains mono"]));
    expect(a.calls).toEqual([]);
    expect(c.calls).toEqual([]);
  });

  it("rebuilds every canvas-backed pane when the arrived families are unknown", () => {
    const a = fake("webgl", {}, "monospace");
    const b = fake("webgl", {}, "monospace");
    const c = fake("canvas", undefined, "monospace");
    const d = fake("dom", undefined, "monospace");
    rebuildAtlasesFor([a.t, b.t, c.t, d.t], null);
    expect(a.calls).toEqual(["clearTextureAtlas"]);
    expect(b.calls).toEqual(["clearTextureAtlas"]);
    expect(c.calls).toEqual(["clearTextureAtlas"]);
    expect(d.calls).toEqual([]);
  });
});
