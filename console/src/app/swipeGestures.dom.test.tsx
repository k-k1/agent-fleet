import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installSwipeGestures, LONG_PRESS_MS } from "./swipeGestures.ts";
import type { SwipeSurfaces } from "./swipeGestures.ts";

// jsdom does not implement TouchEvent, so dispatch an event carrying only what the
// handlers actually read: touches[0]'s coordinates, and the target.
function touchEvent(type: string, x: number, y: number, target: Element, extra: [number, number][] = []): Event {
  const e = new Event(type, { bubbles: true });
  const touches = [{ clientX: x, clientY: y }, ...extra.map(([ex, ey]) => ({ clientX: ex, clientY: ey }))];
  Object.defineProperty(e, "touches", { value: touches });
  target.dispatchEvent(e);
  return e;
}

interface Harness {
  surfaces: SwipeSurfaces;
  calls: string[];
  state: { phone: boolean; coarse: boolean; modal: boolean; drawer: boolean; rail: boolean; rotatable: boolean };
  target: HTMLElement;
  uninstall: () => void;
}

let h: Harness;

function setup(over: Partial<Harness["state"]> = {}): Harness {
  const calls: string[] = [];
  const state = { phone: true, coarse: true, modal: false, drawer: false, rail: false, rotatable: true, ...over };
  const target = document.createElement("div");
  document.body.appendChild(target);
  const surfaces: SwipeSurfaces = {
    phone: () => state.phone,
    coarse: () => state.coarse,
    modal: () => state.modal,
    drawerOpen: () => state.drawer,
    railOpen: () => state.rail,
    rotatable: () => state.rotatable,
    setDrawer: (open) => calls.push(open ? "drawer:open" : "drawer:close"),
    openRailOverlay: () => calls.push("rail:open"),
    closeRail: () => calls.push("rail:close"),
    rotateSession: (delta) => calls.push(delta > 0 ? "rotate:next" : "rotate:prev"),
  };
  const uninstall = installSwipeGestures(window, surfaces);
  return { surfaces, calls, state, target, uninstall };
}

/** Move from (x,y) to (x+dx, y+dy) in a single step. */
function swipe(from: [number, number], dx: number, dy = 0): void {
  touchEvent("touchstart", from[0], from[1], h.target);
  touchEvent("touchmove", from[0] + dx, from[1] + dy, h.target);
  touchEvent("touchend", from[0] + dx, from[1] + dy, h.target);
}

/** Pinch-zoom the page by `factor`: jsdom has no visualViewport, and zoom() reads it as
 * innerWidth / vv.width (see viewport.ts on why not vv.scale). */
function setPageZoom(factor: number): void {
  Object.defineProperty(window, "visualViewport", {
    // A getter, because tests change innerWidth after arranging the zoom.
    value: {
      get width() {
        return window.innerWidth / factor;
      },
    },
    configurable: true,
  });
}

beforeEach(() => {
  // Phone-sized width; the left-third test is min(innerWidth*0.33, 160).
  Object.defineProperty(window, "innerWidth", { value: 390, configurable: true });
  setPageZoom(1);
});

afterEach(() => {
  h?.uninstall();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("left pane show/hide", () => {
  it("phone: swiping right from the left edge opens the drawer", () => {
    h = setup();
    swipe([10, 300], 80);
    expect(h.calls).toEqual(["drawer:open"]);
  });

  it("phone: with the drawer open a left swipe closes it, and does not rotate", () => {
    h = setup({ drawer: true });
    swipe([200, 300], -120);
    expect(h.calls).toEqual(["drawer:close"]);
  });

  it("phone: a right swipe from the left edge belongs to the drawer, not the previous session", () => {
    h = setup();
    swipe([10, 300], 120);
    expect(h.calls).toEqual(["drawer:open"]);
  });

  it("tablet (touch device wider than a phone) shows and hides the rail as an overlay", () => {
    h = setup({ phone: false, coarse: true });
    Object.defineProperty(window, "innerWidth", { value: 1024, configurable: true });
    swipe([20, 300], 80);
    expect(h.calls).toEqual(["rail:open"]);
    h.state.rail = true;
    swipe([500, 300], -80);
    expect(h.calls).toEqual(["rail:open", "rail:close"]);
  });

  it("mouse machines (wide, non-touch screens) are inert", () => {
    h = setup({ phone: false, coarse: false });
    swipe([10, 300], 80);
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });

  it("while a modal is up the rail behind it is left alone", () => {
    h = setup({ modal: true });
    swipe([10, 300], 80);
    expect(h.calls).toEqual([]);
  });
});

describe("phone horizontal swipe = rotate through running sessions", () => {
  it("a left swipe with the drawer closed advances to the next session", () => {
    h = setup();
    swipe([300, 300], -120);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("a right swipe away from the left edge goes back to the previous session", () => {
    h = setup();
    swipe([300, 300], 120);
    expect(h.calls).toEqual(["rotate:prev"]);
  });

  it("a left swipe from the left edge still rotates, coexisting with the pending right swipe", () => {
    h = setup();
    swipe([10, 300], -120);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("with the drawer open a right swipe does nothing; only the closing left swipe is taken", () => {
    h = setup({ drawer: true });
    swipe([300, 300], 120);
    expect(h.calls).toEqual([]);
  });

  it("a sideways wobble short of 70px does not fire (farther than the rail's 50px)", () => {
    h = setup();
    swipe([300, 300], -60);
    expect(h.calls).toEqual([]);
    swipe([300, 300], 60);
    expect(h.calls).toEqual([]);
  });

  it("vertical wins: a diagonal with more vertical travel yields to scrolling", () => {
    h = setup();
    swipe([300, 300], -120, -200);
    swipe([300, 300], 120, 200);
    expect(h.calls).toEqual([]);
  });

  it("fires once per gesture; sliding the finger further does not fire again", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchmove", 180, 300, h.target);
    touchEvent("touchmove", 40, 300, h.target);
    touchEvent("touchend", 40, 300, h.target);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("swinging back the other way with the same finger still settles only once", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchmove", 440, 300, h.target);
    touchEvent("touchmove", 100, 300, h.target);
    touchEvent("touchend", 100, 300, h.target);
    expect(h.calls).toEqual(["rotate:prev"]);
  });

  it("passing the long-press window cancels the candidate, so a text-selection drag is not mistaken for a swipe", () => {
    vi.useFakeTimers();
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    vi.advanceTimersByTime(LONG_PRESS_MS + 1);
    touchEvent("touchmove", 150, 300, h.target);
    expect(h.calls).toEqual([]);
  });

  it("stands down when the gesture starts on a surface that owns horizontal interaction, such as an input", () => {
    h = setup();
    const input = document.createElement("textarea");
    h.target.appendChild(input);
    touchEvent("touchstart", 300, 300, input);
    touchEvent("touchmove", 150, 300, input);
    expect(h.calls).toEqual([]);
  });

  // Regression guard: pinch-zooming an image and dragging it around switched sessions.
  // Spreading two fingers moves touches[0] far sideways, which the recognizer used to
  // read as a swipe.
  it("a pinch does not rotate: the second finger cancels the candidate", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchstart", 300, 300, h.target, [[320, 300]]);
    touchEvent("touchmove", 160, 300, h.target, [[460, 300]]);
    touchEvent("touchmove", 40, 300, h.target, [[560, 300]]);
    expect(h.calls).toEqual([]);
  });

  it("a gesture that started with one finger is dropped as soon as a second lands", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target);
    touchEvent("touchmove", 260, 300, h.target); // short of the threshold, still a candidate
    touchEvent("touchmove", 150, 300, h.target, [[400, 300]]);
    expect(h.calls).toEqual([]);
  });

  it("after a pinch, lifting back to one finger does not revive the candidate", () => {
    h = setup();
    touchEvent("touchstart", 300, 300, h.target, [[320, 300]]);
    touchEvent("touchend", 300, 300, h.target);
    touchEvent("touchmove", 100, 300, h.target);
    expect(h.calls).toEqual([]);
  });

  // Pinch-zooming the page (a PDF page, a markdown figure — anything that does not set
  // touch-action) and then dragging to see the rest of it is a viewport pan, not a swipe.
  it("while the page is pinch-zoomed nothing is recognised", () => {
    h = setup();
    setPageZoom(2);
    swipe([300, 300], -120);
    swipe([300, 300], 120);
    swipe([10, 300], 120); // the drawer's edge swipe is a pan there too
    expect(h.calls).toEqual([]);
    setPageZoom(1);
    swipe([300, 300], -120);
    expect(h.calls).toEqual(["rotate:next"]);
  });

  it("a popped-out tab (rotatable=false) does not rotate", () => {
    h = setup({ rotatable: false });
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });

  it("a tablet does not rotate on left/right swipes; that is a phone-only gesture", () => {
    h = setup({ phone: false, coarse: true });
    swipe([500, 300], -120);
    swipe([500, 300], 120);
    expect(h.calls).toEqual([]);
  });

  it("after uninstall, events are no longer picked up", () => {
    h = setup();
    h.uninstall();
    swipe([300, 300], -120);
    expect(h.calls).toEqual([]);
  });
});
