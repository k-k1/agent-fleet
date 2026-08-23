import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { wireViewport } from "./viewport";

// A pinch zoom shrinks the visual viewport exactly like the soft keyboard does, and
// the frame-fitting fallback used to take the bait: zooming into the mirror on a
// phone set --app-h to the zoomed viewport height, so the whole app shell collapsed
// and the composer pinned to its bottom edge landed in the middle of the screen,
// over the text the user had just zoomed in to read.
class FakeVisualViewport {
  height: number;
  width: number;
  offsetTop = 0;
  scale = 1;
  private listeners = new Map<string, Set<() => void>>();
  constructor(height: number, width: number) {
    this.height = height;
    this.width = width;
  }
  /** Pinch to `factor`: both axes shrink, only the height is keyboard-relevant. */
  pinch(factor: number, visibleLayoutPx = LAYOUT_H) {
    this.scale = factor;
    this.width = LAYOUT_W / factor;
    this.height = visibleLayoutPx / factor;
  }
  addEventListener(type: string, fn: () => void) {
    let set = this.listeners.get(type);
    if (!set) this.listeners.set(type, (set = new Set()));
    set.add(fn);
  }
  removeEventListener(type: string, fn: () => void) {
    this.listeners.get(type)?.delete(fn);
  }
  emit(type: string) {
    for (const fn of [...(this.listeners.get(type) ?? [])]) fn();
  }
}

const LAYOUT_H = 800; // window.innerHeight
const LAYOUT_W = 400; // window.innerWidth — a keyboard never changes it
const KEYBOARD = 400; // a phone keyboard, well over the 150px threshold

// wireViewport() also listens on window, and jsdom keeps those listeners for the
// whole file. Every viewport it was given is parked back at "no keyboard, no zoom"
// after each test so a stale instance can't answer a later test's scroll event.
const parked: FakeVisualViewport[] = [];

function wire(vv: FakeVisualViewport) {
  Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
  parked.push(vv);
  wireViewport();
}

const appH = () => document.documentElement.style.getPropertyValue("--app-h");

beforeEach(() => {
  Object.defineProperty(window, "innerHeight", { value: LAYOUT_H, configurable: true });
  Object.defineProperty(window, "innerWidth", { value: LAYOUT_W, configurable: true });
  document.documentElement.style.removeProperty("--app-h");
});

afterEach(() => {
  for (const vv of parked.splice(0)) {
    vv.pinch(1);
    vv.emit("resize");
  }
  document.documentElement.style.removeProperty("--app-h");
});

describe("wireViewport", () => {
  it("leaves the frame alone while the page is only pinch-zoomed", () => {
    const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
    wire(vv);

    vv.pinch(2); // the visual viewport now covers half the layout, keyboard or not
    vv.emit("resize");

    expect(appH()).toBe("");
  });

  it("leaves the frame alone on a page whose initial scale isn't 1", () => {
    const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
    wire(vv);

    // Measured in Chromium: a page laid out at 980px on a 400px screen reports
    // scale 0.408 with no zoom applied, and the whole layout viewport visible.
    // Reading the zoom off vv.scale would invent a keyboard here.
    vv.scale = 0.408;
    vv.emit("resize");

    expect(appH()).toBe("");
  });

  it("fits the frame above the keyboard", () => {
    const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
    wire(vv);

    vv.height = LAYOUT_H - KEYBOARD;
    vv.emit("resize");

    expect(appH()).toBe(`${LAYOUT_H - KEYBOARD}px`);
  });

  it("fits the frame in LAYOUT px when the keyboard opens on a zoomed page", () => {
    const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
    wire(vv);

    vv.pinch(2, LAYOUT_H - KEYBOARD);
    vv.emit("resize");

    // The zoom must not shrink the layout: the shell still spans the whole area the
    // keyboard leaves free, not the 200px the visual viewport happens to cover.
    expect(appH()).toBe(`${LAYOUT_H - KEYBOARD}px`);
  });

  it("drops the fit as soon as the keyboard closes", () => {
    const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
    wire(vv);

    vv.height = LAYOUT_H - KEYBOARD;
    vv.emit("resize");
    expect(appH()).not.toBe("");

    vv.height = LAYOUT_H;
    vv.emit("resize");
    expect(appH()).toBe("");
  });

  describe("the focus auto-scroll re-pin", () => {
    let scrollTo: ReturnType<typeof vi.fn>;

    beforeEach(() => {
      scrollTo = vi.fn();
      window.scrollTo = scrollTo as unknown as typeof window.scrollTo;
      Object.defineProperty(window, "scrollY", { value: 120, configurable: true });
    });

    it("undoes the browser's pan with the keyboard up and no zoom", () => {
      const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
      wire(vv);
      vv.height = LAYOUT_H - KEYBOARD;
      vv.emit("resize");

      window.dispatchEvent(new Event("scroll"));

      expect(scrollTo).toHaveBeenCalledWith(0, 0);
    });

    it("lets a zoomed page stay where the user panned it", () => {
      const vv = new FakeVisualViewport(LAYOUT_H, LAYOUT_W);
      wire(vv);
      vv.pinch(2, LAYOUT_H - KEYBOARD);
      vv.emit("resize");

      window.dispatchEvent(new Event("scroll"));

      expect(scrollTo).not.toHaveBeenCalled();
    });
  });
});
