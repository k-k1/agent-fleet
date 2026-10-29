// Glyph atlas and render-model resets that stay safe when terminals share one atlas.
//
// @xterm/addon-webgl hands every terminal with an equal atlas config (font, size, DPR,
// colors — so every pane with the same background tint) one shared TextureAtlas. Its
// clearTextureAtlas() wipes and repacks that atlas but rebuilds only the caller's render
// model. Every sibling keeps the per-cell texture coordinates of its unchanged cells,
// re-uploads the repacked pages on its next frame and draws those cells from whatever
// glyph now sits at the old coordinates: the "garbled pane that heals on click" (#2983).
// A wiped atlas also has to re-rasterize every glyph each sharer shows, which takes
// seconds of main thread with a few panes of CJK text. So routine repaints reset only the
// model (resetGlyphModel), and the atlas is cleared only when its bitmaps really are stale
// (rebuildGlyphAtlas). The 2D canvas addon is a separate bundle with its own cache and
// looks glyphs up on every draw, so it is outside all of this.

import type { Terminal } from "@xterm/xterm";

export interface AtlasTerm {
  term?: Terminal | null;
  webgl?: object | null;
  canvas?: object | null;
}

/* eslint-disable @typescript-eslint/no-explicit-any */

// webglAtlas returns the TextureAtlas a WebGL renderer draws from, or undefined when the
// private reach-in no longer matches (an xterm bump): callers then treat it as shared.
function webglAtlas(t: AtlasTerm): unknown {
  try {
    return (t.webgl as any)?._renderer?._charAtlas ?? undefined;
  } catch {
    return undefined;
  }
}

// resetGlyphModel drops a WebGL pane's render model so its next frame resolves every cell
// against the atlas again, reusing the glyphs already rasterized there. It resets CPU-side
// arrays only and never blanks the canvas. Follow it with a full refresh in the same task:
// clear() schedules just the cursor row, and a frame drawn from the empty model in between
// would drop every other cell.
export function resetGlyphModel(t: AtlasTerm): void {
  if (!t.webgl || !t.term) return;
  try {
    (t.term as any)._core?._renderService?.clear?.();
  } catch {}
}

// rebuildGlyphAtlas clears `it`'s glyph atlas and rebuilds the model of every WebGL sibling
// drawing from the same atlas, so none of them is left on stale coordinates. Costly: every
// sharer's glyphs are re-rasterized. Keep it for genuinely stale bitmaps.
export function rebuildGlyphAtlas(it: AtlasTerm, all: Iterable<AtlasTerm>): void {
  if (!it.term || !(it.webgl || it.canvas)) return;
  (it.term as any).clearTextureAtlas?.();
  if (!it.webgl) return;
  const atlas = webglAtlas(it);
  for (const other of all) {
    if (other === it || !other.webgl || !other.term) continue;
    const theirs = webglAtlas(other);
    // A known, different atlas was not touched. When in doubt, rebuild: a needless model
    // rebuild costs a lookup per cell, a missed one leaves the pane garbled.
    if (atlas && theirs && atlas !== theirs) continue;
    try {
      resetGlyphModel(other);
      other.term.refresh(0, other.term.rows - 1);
    } catch {}
  }
}

// fontFamilies splits a CSS font-family list into bare, lower-cased family names, the form
// FontFace.family is compared in.
export function fontFamilies(stack: string): string[] {
  return stack
    .split(",")
    .map((f) => f.trim().replace(/^["']|["']$/g, "").toLowerCase())
    .filter(Boolean);
}

// rebuildAtlasesFor rebuilds, once per shared atlas, the atlas of every pane whose font list
// names one of the `arrived` families (lower-case; null = any). Glyphs drawn while that face
// was still loading are fallback-font bitmaps, and nothing else replaces them: xterm does not
// watch document.fonts, and routine repaints keep the atlas on purpose.
export function rebuildAtlasesFor(all: AtlasTerm[], arrived: Set<string> | null): void {
  const done = new Set<unknown>();
  for (const t of all) {
    if (!t.term || !(t.webgl || t.canvas)) continue;
    if (arrived && !fontFamilies(String(t.term.options.fontFamily ?? "")).some((f) => arrived.has(f))) continue;
    const atlas = t.webgl ? webglAtlas(t) : undefined;
    if (atlas && done.has(atlas)) continue;
    rebuildGlyphAtlas(t, all);
    if (atlas) done.add(atlas);
  }
}

/* eslint-enable @typescript-eslint/no-explicit-any */
