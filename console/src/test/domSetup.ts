// Setup for the `dom` vitest project (see vite.config.js).
//
// jsdom implements the DOM tree but not layout: every box is 0x0 and Range has
// no client rects at all. CodeMirror measures while it mounts, so without these
// stubs a component test that renders the editor dies on
// `textRange(...).getClientRects is not a function`.
//
// These give measurement something to read; they do NOT make it meaningful.
// Anything that depends on real geometry — the send pill's position, scroll
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

// jsdom has no ResizeObserver either. Components that publish their own height as a CSS
// variable (Section and friends) always construct one at mount, so this is a shell that
// observes nothing. Boxes stay 0x0, so as above anything depending on real size still needs a
// real browser.
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  } as unknown as typeof ResizeObserver;
}

// jsdom cannot make Blob URLs (URL.createObjectURL is unimplemented). The composer that holds
// attachments calls it the moment something is pasted, so without this the mount itself dies.
// The shell only hands out countable URLs: they resolve to nothing, so whether an image
// actually renders cannot be measured here (that is a real browser's job). Mixed-up URLs and
// missing revokes can still be checked from which URL was created and which was revoked.
if (typeof URL !== "undefined" && typeof URL.createObjectURL !== "function") {
  let n = 0;
  URL.createObjectURL = () => `blob:af-test/${++n}`;
  URL.revokeObjectURL = () => {};
}

// matchMedia is missing too. device.ts calls it unguarded, and xterm calls it inside open() to
// read devicePixelRatio (without it Terminal.open throws). This shell always returns
// matches:false, i.e. desktop / fine pointer. jsdom has neither layout nor media, so the real
// device branching cannot be verified here.
if (typeof window !== "undefined" && typeof window.matchMedia !== "function") {
  window.matchMedia = ((query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia;
}
