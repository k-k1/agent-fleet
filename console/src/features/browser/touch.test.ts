import { describe, expect, it } from "vitest";
import type { BrowserOutbound } from "./protocol.ts";
import { BrowserTouchGestures, TOUCH_LONG_PRESS_MS, type BrowserTouchHost } from "./touch.ts";

/**
 * A manual clock plus a timer queue: the recognizer's long press, flick decay and
 * velocity all read the clock, so real timers would make the assertions racy.
 */
class Harness {
  readonly sent: BrowserOutbound[] = [];
  readonly previews: Array<{ factor: number; x: number; y: number }> = [];
  readonly zooms: number[] = [];
  toggles = 0;
  readonly anchored: Array<{ x: number; y: number }> = [];
  enabled = true;
  scale = 2; // 2 remote px per CSS px
  private clock = 1000;
  private handle = 0;
  private readonly timers = new Map<number, { at: number; run: () => void }>();
  readonly gestures: BrowserTouchGestures;

  constructor() {
    const host: BrowserTouchHost = {
      remote: (x, y) => ({ x: x * this.scale, y: y * this.scale }),
      scale: () => this.scale,
      enabled: () => this.enabled,
      send: (message) => this.sent.push(message),
      anchor: (x, y) => this.anchored.push({ x, y }),
      preview: (factor, x, y) => this.previews.push({ factor, x, y }),
      zoom: (factor) => this.zooms.push(factor),
      toggleZoom: () => { this.toggles++; },
      now: () => this.clock,
      after: (ms, run) => {
        const id = ++this.handle;
        this.timers.set(id, { at: this.clock + ms, run });
        return id;
      },
      clear: (id) => { this.timers.delete(id); },
    };
    this.gestures = new BrowserTouchGestures(host);
  }

  /** Advance the clock, firing due timers in order (they may queue more). */
  tick(ms: number): void {
    const until = this.clock + ms;
    for (;;) {
      let next: [number, { at: number; run: () => void }] | null = null;
      for (const entry of this.timers) {
        if (entry[1].at <= until && (!next || entry[1].at < next[1].at)) next = entry;
      }
      if (!next) break;
      this.timers.delete(next[0]);
      this.clock = next[1].at;
      next[1].run();
    }
    this.clock = until;
  }

  down(pointerId: number, clientX: number, clientY: number): void {
    this.gestures.down({ pointerId, clientX, clientY });
  }
  move(pointerId: number, clientX: number, clientY: number, ms = 16): void {
    this.tick(ms);
    this.gestures.move({ pointerId, clientX, clientY });
  }
  up(pointerId: number, clientX: number, clientY: number): void {
    this.gestures.up({ pointerId, clientX, clientY });
  }

  wheels(): Array<{ deltaX: number; deltaY: number }> {
    return this.sent.flatMap((m) => (m.type === "wheel" ? [{ deltaX: m.deltaX, deltaY: m.deltaY }] : []));
  }
  mouse(): Array<{ event: string; button: string; clickCount: number; x: number; y: number }> {
    return this.sent.flatMap((m) =>
      m.type === "mouse" ? [{ event: m.event, button: m.button, clickCount: m.clickCount, x: m.x, y: m.y }] : []);
  }
}

describe("touch gestures", () => {
  // Forwarding a swipe as a mouse drag is what made the pane unusable on a phone:
  // the page selected text (or dragged an element) and never scrolled.
  it("scrolls on a swipe instead of dragging the page", () => {
    const h = new Harness();
    h.down(1, 100, 400);
    h.move(1, 100, 380);
    h.move(1, 100, 340);
    h.up(1, 100, 340);

    expect(h.mouse()).toEqual([]);
    // Dragging the content UP scrolls DOWN, in remote pixels (scale 2). Anything
    // after the two moves is the release momentum, asserted separately below.
    expect(h.wheels().slice(0, 2).map((w) => w.deltaY)).toEqual([40, 80]);
    expect(h.wheels().every((w) => w.deltaX === 0)).toBe(true);
  });

  // A finger reports 60-120 moves a second and every one of them would cross the
  // Console, the Control Plane and the Agent. The screencast runs at ~12 fps, so
  // the deltas within a frame are summed into one wheel instead.
  it("sums the moves inside one frame into a single wheel", () => {
    const h = new Harness();
    h.down(1, 100, 400);
    h.move(1, 100, 380, 4); // leading edge: goes out at once
    h.move(1, 100, 370, 4);
    h.move(1, 100, 360, 4);
    expect(h.wheels().map((w) => w.deltaY)).toEqual([40]);

    h.tick(16); // the frame closes and the batched remainder follows
    expect(h.wheels().map((w) => w.deltaY)).toEqual([40, 40]);

    // Nothing pending: an idle finger must not keep a timer alive.
    h.tick(200);
    expect(h.wheels()).toHaveLength(2);
  });

  it("keeps scrolling with momentum after a flick, then stops", () => {
    const h = new Harness();
    h.down(1, 100, 500);
    for (let i = 1; i <= 5; i++) h.move(1, 100, 500 - i * 40);
    const duringSwipe = h.wheels().length;
    h.up(1, 100, 300);

    h.tick(80);
    const afterFlick = h.wheels().length;
    expect(afterFlick).toBeGreaterThan(duringSwipe);
    expect(h.wheels().slice(duringSwipe).every((w) => w.deltaY > 0)).toBe(true);

    h.tick(4000);
    const settled = h.wheels().length;
    h.tick(4000);
    expect(h.wheels().length).toBe(settled);
  });

  // A tap has to arrive as a real click; the leading hover move is what makes
  // menus that open on mouseover reachable without a mouse.
  // The hidden input follows the tap (that is where a composition popup shows)
  // but is NOT focused: focusing raises the on-screen keyboard, and a tap is
  // usually aimed at a link, not a text field.
  it("clicks on a tap and moves the input anchor there", () => {
    const h = new Harness();
    h.down(1, 60, 70);
    h.move(1, 62, 71);
    h.up(1, 62, 71);

    expect(h.mouse()).toEqual([
      { event: "move", button: "none", clickCount: 0, x: 124, y: 142 },
      { event: "down", button: "left", clickCount: 1, x: 124, y: 142 },
      { event: "up", button: "left", clickCount: 1, x: 124, y: 142 },
    ]);
    expect(h.anchored).toEqual([{ x: 62, y: 71 }]);
  });

  // Double tap is the mobile idiom for "fit <-> life size". It sends no second
  // click: delaying the first one to find out whether a second is coming is the
  // 300 ms tap lag the platform spent years removing.
  it("toggles the zoom on a double tap without clicking twice", () => {
    const h = new Harness();
    const tap = (x: number) => { h.down(1, x, 70); h.up(1, x, 70); };
    tap(60);
    h.tick(120);
    tap(61);

    expect(h.toggles).toBe(1);
    expect(h.mouse().filter((m) => m.event === "down")).toHaveLength(1);

    // A third tap in the same burst must not toggle straight back.
    h.tick(120);
    tap(61);
    expect(h.toggles).toBe(1);

    // Two slow taps are two ordinary clicks.
    h.tick(2000);
    tap(61);
    expect(h.toggles).toBe(1);
    expect(h.mouse().filter((m) => m.event === "down")).toHaveLength(3);
  });

  it("toggles the zoom on a double tap even while page input is refused", () => {
    const h = new Harness();
    h.enabled = false;
    h.down(1, 60, 70);
    h.up(1, 60, 70);
    h.tick(120);
    h.down(1, 61, 70);
    h.up(1, 61, 70);

    expect(h.toggles).toBe(1);
    expect(h.sent).toEqual([]);
  });

  // Text selection, sliders and drag & drop are unreachable on touch unless the
  // button really goes down — a long press is the only spare gesture for it.
  it("promotes a long press to a button-down drag", () => {
    const h = new Harness();
    h.down(1, 60, 70);
    h.tick(TOUCH_LONG_PRESS_MS);
    expect(h.mouse().at(-1)).toMatchObject({ event: "down", button: "left" });

    h.move(1, 160, 70);
    expect(h.mouse().at(-1)).toMatchObject({ event: "move", button: "left", x: 320 });
    expect(h.wheels()).toEqual([]);

    h.up(1, 160, 70);
    expect(h.mouse().at(-1)).toMatchObject({ event: "up", button: "left" });
  });

  it("previews a pinch while the fingers are down and commits the factor once", () => {
    const h = new Harness();
    h.down(1, 100, 300);
    h.down(2, 200, 300);
    h.move(1, 50, 300);
    h.move(2, 250, 300);

    expect(h.previews.at(-1)).toMatchObject({ factor: 2, x: 150, y: 300 });
    expect(h.zooms).toEqual([]);

    h.up(1, 50, 300);
    expect(h.zooms).toEqual([2]);
    // The transform has to be released or the canvas stays scaled over the
    // freshly relaid-out frames.
    expect(h.previews.at(-1)?.factor).toBe(1);

    // The finger still down must not become a pan: the page would jump by the
    // distance the fingers had spread.
    h.move(2, 250, 200);
    expect(h.wheels()).toEqual([]);
    h.up(2, 250, 200);
    expect(h.mouse()).toEqual([]);
  });

  it("does not commit a zoom for a resting two-finger hold", () => {
    const h = new Harness();
    h.down(1, 100, 300);
    h.down(2, 200, 300);
    h.move(1, 101, 300);
    h.up(1, 101, 300);
    expect(h.zooms).toEqual([]);
  });

  // Zoom is the viewer's own layout viewport, not page input, so it stays
  // available while the attachment is view-only — that is the whole point of a
  // pane you are only allowed to read.
  it("still pinches while page input is refused, but sends nothing else", () => {
    const h = new Harness();
    h.enabled = false;
    h.down(1, 100, 400);
    h.move(1, 100, 300);
    h.up(1, 100, 300);
    h.tick(2000);
    expect(h.sent).toEqual([]);

    h.down(1, 100, 300);
    h.down(2, 200, 300);
    h.move(1, 100, 300);
    h.move(2, 300, 300);
    h.up(2, 300, 300);
    expect(h.zooms).toEqual([2]);
    expect(h.sent).toEqual([]);
  });

  it("drops the gesture on pointercancel without clicking", () => {
    const h = new Harness();
    h.down(1, 60, 70);
    h.gestures.cancel({ pointerId: 1, clientX: 60, clientY: 70 });
    h.tick(2000);
    expect(h.sent).toEqual([]);
    expect(h.anchored).toEqual([]);
  });
});
