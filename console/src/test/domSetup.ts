// Setup for the `dom` vitest project (see vite.config.js).
//
// jsdom implements the DOM tree but not layout: every box is 0x0 and Range has
// no client rects at all. CodeMirror measures while it mounts, so without these
// stubs a component test that renders the editor dies on
// `textRange(...).getClientRects is not a function`.
//
// These give measurement something to read; they do NOT make it meaningful.
// Anything that depends on real geometry — the 送る pill's position, scroll
// offsets, the visible target-line row — cannot be verified here and still needs
// a real browser.

const emptyRectList = (): DOMRectList => {
  const list: DOMRect[] = [];
  return Object.assign(list, { item: (i: number) => list[i] ?? null }) as unknown as DOMRectList;
};

if (typeof Range !== "undefined") {
  Range.prototype.getClientRects = emptyRectList;
  Range.prototype.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0);
}

if (typeof Element !== "undefined" && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// jsdom にも ResizeObserver は無い。Section など「自分の高さを CSS 変数に公開する」
// 部品が mount 時に必ず生成するので、観測しないだけの器を置く（0x0 のままなので
// 実寸に依存する検証は上記のとおり本物のブラウザが要る）。
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  } as unknown as typeof ResizeObserver;
}
