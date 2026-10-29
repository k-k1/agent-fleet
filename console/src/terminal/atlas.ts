// Glyph atlas rebuilds that stay safe when several terminals share one atlas.
//
// @xterm/addon-webgl hands every terminal with an equal atlas config (font, size, DPR,
// colors — so every pane with the same background tint) the SAME TextureAtlas. Its
// clearTextureAtlas() wipes and repacks that shared atlas but rebuilds only the CALLER's
// render model. Every sibling keeps the per-cell texture coordinates of its unchanged
// cells, re-uploads the repacked pages on its next frame and draws those cells from
// whatever glyph now sits at the old coordinates: the "garbled pane that heals on click"
// (#2983). The 2D canvas addon is a separate bundle with its own cache and looks glyphs up
// on every draw, so only WebGL siblings need the rebuild.

import type { Terminal } from "@xterm/xterm";

export interface AtlasTerm {
  term?: Terminal | null;
  webgl?: object | null;
  canvas?: object | null;
}

// webglAtlas returns the TextureAtlas a WebGL renderer draws from, or undefined when the
// private reach-in no longer matches (an xterm bump): callers then treat it as shared.
function webglAtlas(t: AtlasTerm): unknown {
  try {
    /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
    return (t.webgl as any)?._renderer?._charAtlas ?? undefined;
  } catch {
    return undefined;
  }
}

// rebuildGlyphAtlas clears `it`'s glyph atlas and rebuilds the render model of every WebGL
// sibling drawing from the same atlas. The sibling rebuild is _renderService.clear() — on
// WebGL it only resets CPU-side model/vertex arrays and never blanks the canvas — followed
// at once by a full refresh: clear() alone schedules just the cursor row, and a frame
// rendered from an empty model in between would drop every other cell.
export function rebuildGlyphAtlas(it: AtlasTerm, all: Iterable<AtlasTerm>): void {
  if (!it.term || !(it.webgl || it.canvas)) return;
  /* eslint-disable @typescript-eslint/no-explicit-any */
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
      (other.term as any)._core?._renderService?.clear?.();
      other.term.refresh(0, other.term.rows - 1);
    } catch {}
  }
  /* eslint-enable @typescript-eslint/no-explicit-any */
}
