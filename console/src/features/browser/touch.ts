import type { BrowserOutbound } from "./protocol.ts";

/**
 * Touch gestures for the screencast surface.
 *
 * A finger is NOT a mouse. Forwarding pointerdown/move/up straight through as
 * mouse press/move/release — which is what the surface used to do — means every
 * swipe is a button-down drag: the remote page selects text or drags an element
 * instead of scrolling, and the page never moves. On a phone that leaves the
 * pane effectively unreadable past the first screen.
 *
 * So touch is recognised before it is forwarded:
 * - a swipe scrolls (wheel), with flick momentum after the finger lifts;
 * - a tap clicks (with a leading hover move, so menus that open on hover work);
 * - a long press starts a real button-down drag — text selection, sliders,
 *   drag & drop — which is otherwise unreachable without a mouse;
 * - two fingers pinch to zoom, which is a VIEWER concern (the layout viewport),
 *   not page input, so it is allowed even while the page is view-only.
 *
 * Everything here works in the canvas's CSS pixels; the host maps to the remote
 * page's coordinate space, which with zoom-to-fit is a different scale.
 */

/** The slice of a PointerEvent this recognizer needs. */
export interface BrowserTouchPoint {
  pointerId: number;
  clientX: number;
  clientY: number;
}

export interface BrowserTouchHost {
  /** Map a client point into the remote page's coordinate space. */
  remote(clientX: number, clientY: number): { x: number; y: number };
  /** Remote pixels per CSS pixel (the canvas is stretched to fill the pane). */
  scale(): number;
  /** False while the page rejects input (view-only / locked). Zoom is unaffected. */
  enabled(): boolean;
  send(message: BrowserOutbound): void;
  /**
   * A tap landed at this client point. It moves the hidden IME input there (that
   * is where an on-screen keyboard puts its composition popup) but deliberately
   * does NOT focus it: focusing is what pops the keyboard up, and a tap is
   * usually aimed at a link or a button, not a text field.
   */
  anchor(clientX: number, clientY: number): void;
  /**
   * Live pinch feedback while the fingers are still down, as a scale factor and
   * its origin in CLIENT coordinates. The committed zoom is a round trip to the
   * Agent (relayout + screencast restart), far too slow to run per frame, so the
   * canvas is scaled locally until the fingers lift.
   */
  preview(factor: number, originX: number, originY: number): void;
  /** Commit a pinch: multiply the current zoom by this factor. */
  zoom(factor: number): void;
  /** Double tap: jump between the fitted view and life size. */
  toggleZoom(): void;
  now(): number;
  after(ms: number, callback: () => void): number;
  clear(handle: number): void;
}

/** Movement past this (CSS px) is a swipe, not a tap. */
export const TOUCH_TAP_SLOP = 10;
/** Holding still this long turns the touch into a button-down drag. */
export const TOUCH_LONG_PRESS_MS = 500;
/** A second tap inside this window (and TOUCH_TAP_SLOP*3 px) is a double tap. */
export const TOUCH_DOUBLE_TAP_MS = 350;
/** Below this release speed (CSS px/ms) a swipe just stops — no momentum. */
export const TOUCH_FLICK_MIN_SPEED = 0.12;
const TOUCH_FLICK_STOP_SPEED = 0.02;
const TOUCH_FLICK_DECAY = 0.95;
/**
 * Momentum ticks at half the rate the decay is expressed in: the screencast the
 * user is watching runs at ~12 fps, so a finer tick would only put more messages
 * on the wire for a picture that cannot show them. The decay stays referenced to
 * 16 ms so the fling travels the same distance either way.
 */
const TOUCH_FLICK_TICK_MS = 32;
const TOUCH_FLICK_DECAY_REF_MS = 16;
/** Scroll deltas are summed into one wheel per frame. */
const TOUCH_PAN_FRAME_MS = 16;
/** Two fingers closer than this cannot give a stable pinch ratio. */
const TOUCH_PINCH_MIN_SPAN = 24;
/** Ratio change below this is a resting hand, not a pinch. */
const TOUCH_PINCH_DEADZONE = 0.04;

interface Finger {
  x: number;
  y: number;
  startX: number;
  startY: number;
  movedAt: number;
  vx: number;
  vy: number;
}

/**
 * "spent" is the tail of a pinch: fingers are still down but the gesture is
 * decided, and promoting the survivor to a pan would jump the page by whatever
 * distance the fingers had spread.
 */
type Mode = "idle" | "undecided" | "pan" | "drag" | "pinch" | "spent";

export class BrowserTouchGestures {
  private readonly fingers = new Map<number, Finger>();
  private mode: Mode = "idle";
  private longPress = 0;
  private dragPoint = { x: 0, y: 0 };
  private flick = 0;
  private flickAt = 0;
  private flickV = { x: 0, y: 0 };
  private flickPoint = { x: 0, y: 0 };
  private panWindow = 0;
  private panDelta = { dx: 0, dy: 0 };
  private panPoint = { x: 0, y: 0 };
  private pinchSpan = 0;
  private pinchFactor = 1;
  private lastTapAt = -Infinity;
  private lastTap = { x: 0, y: 0 };

  constructor(private readonly host: BrowserTouchHost) {}

  down(point: BrowserTouchPoint): void {
    this.stopFlick();
    this.fingers.set(point.pointerId, {
      x: point.clientX,
      y: point.clientY,
      startX: point.clientX,
      startY: point.clientY,
      movedAt: this.host.now(),
      vx: 0,
      vy: 0,
    });
    if (this.fingers.size === 1) {
      this.mode = "undecided";
      this.longPress = this.host.after(TOUCH_LONG_PRESS_MS, () => this.startDrag());
      return;
    }
    this.cancelLongPress();
    if (this.fingers.size === 2) {
      // A drag that a second finger joins is over: release the button where it
      // is, or the page keeps selecting while the user pinches.
      if (this.mode === "drag") this.sendMouse("up", this.dragPoint, "left", 0, 1);
      this.mode = "pinch";
      this.pinchSpan = this.span();
      this.pinchFactor = 1;
    }
  }

  move(point: BrowserTouchPoint): void {
    const finger = this.fingers.get(point.pointerId);
    if (!finger) return;
    const now = this.host.now();
    const dt = Math.max(1, now - finger.movedAt);
    const dx = point.clientX - finger.x;
    const dy = point.clientY - finger.y;
    finger.x = point.clientX;
    finger.y = point.clientY;
    finger.movedAt = now;
    // Low-pass the velocity: the last delta alone makes a flick land on whatever
    // jitter the final event happened to carry.
    finger.vx = 0.7 * (dx / dt) + 0.3 * finger.vx;
    finger.vy = 0.7 * (dy / dt) + 0.3 * finger.vy;

    if (this.mode === "pinch") {
      this.updatePinch();
      return;
    }
    if (this.mode === "undecided") {
      if (Math.hypot(finger.x - finger.startX, finger.y - finger.startY) <= TOUCH_TAP_SLOP) return;
      this.cancelLongPress();
      this.mode = "pan";
    }
    if (this.mode === "pan") this.pan(dx, dy, finger.x, finger.y);
    else if (this.mode === "drag") {
      this.dragPoint = { x: finger.x, y: finger.y };
      this.sendMouse("move", this.dragPoint, "left", 1, 0);
    }
  }

  up(point: BrowserTouchPoint): void {
    const finger = this.fingers.get(point.pointerId);
    this.fingers.delete(point.pointerId);
    if (!finger) return;
    this.cancelLongPress();
    switch (this.mode) {
      case "pinch":
        if (this.fingers.size >= 2) {
          this.pinchSpan = this.span();
          this.pinchFactor = 1;
          return;
        }
        this.commitPinch();
        this.mode = this.fingers.size > 0 ? "spent" : "idle";
        return;
      case "drag":
        this.sendMouse("up", this.dragPoint, "left", 0, 1);
        break;
      case "pan":
        this.startFlick(finger);
        break;
      case "undecided":
        this.tap(finger);
        break;
    }
    if (this.fingers.size === 0) this.mode = "idle";
  }

  /** The browser took the gesture away (or the surface unmounted): drop it silently. */
  cancel(point: BrowserTouchPoint): void {
    const known = this.fingers.delete(point.pointerId);
    this.cancelLongPress();
    if (!known) return;
    if (this.mode === "drag") this.sendMouse("up", this.dragPoint, "left", 0, 1);
    if (this.mode === "pinch" && this.fingers.size < 2) {
      this.host.preview(1, 0, 0);
      this.pinchFactor = 1;
      this.mode = this.fingers.size > 0 ? "spent" : "idle";
      return;
    }
    if (this.fingers.size === 0) this.mode = "idle";
  }

  dispose(): void {
    this.cancelLongPress();
    this.stopFlick();
    this.stopPan();
    this.fingers.clear();
    this.mode = "idle";
  }

  /**
   * Scroll by a finger delta, at most once per frame.
   *
   * A finger reports 60-120 moves a second, and every message crosses the
   * Console, the Control Plane and the Agent before reaching Chromium. Sending
   * one wheel per move buys nothing — the screencast itself runs at ~12 fps — so
   * the deltas in a frame are summed into one wheel. The first move of a gesture
   * still goes out immediately; only the ones behind it are batched, which keeps
   * the scroll from feeling delayed at the start of a swipe.
   */
  private pan(dx: number, dy: number, clientX: number, clientY: number): void {
    if (!this.host.enabled()) return;
    this.panPoint = { x: clientX, y: clientY };
    if (this.panWindow) {
      this.panDelta.dx += dx;
      this.panDelta.dy += dy;
      return;
    }
    this.sendPan(dx, dy);
    this.panWindow = this.host.after(TOUCH_PAN_FRAME_MS, this.flushPan);
  }

  private readonly flushPan = (): void => {
    this.panWindow = 0;
    const { dx, dy } = this.panDelta;
    if (dx === 0 && dy === 0) return;
    this.panDelta = { dx: 0, dy: 0 };
    this.sendPan(dx, dy);
    this.panWindow = this.host.after(TOUCH_PAN_FRAME_MS, this.flushPan);
  };

  private sendPan(dx: number, dy: number): void {
    const scale = this.host.scale();
    // Dragging the CONTENT up means scrolling DOWN, hence the sign flip.
    this.host.send({
      type: "wheel",
      ...this.host.remote(this.panPoint.x, this.panPoint.y),
      deltaX: -dx * scale,
      deltaY: -dy * scale,
      modifiers: 0,
    });
  }

  private stopPan(): void {
    if (this.panWindow) this.host.clear(this.panWindow);
    this.panWindow = 0;
    this.panDelta = { dx: 0, dy: 0 };
  }

  private tap(finger: Finger): void {
    const now = this.host.now();
    const double = now - this.lastTapAt < TOUCH_DOUBLE_TAP_MS &&
      Math.hypot(finger.x - this.lastTap.x, finger.y - this.lastTap.y) <= TOUCH_TAP_SLOP * 3;
    this.lastTap = { x: finger.x, y: finger.y };
    if (double) {
      // Double tap toggles the zoom, the way every mobile browser does — and it
      // sends NO second click: the first tap already clicked, and delaying that
      // one to find out whether a second is coming is the 300 ms tap lag the
      // whole platform spent years removing. Like the pinch it is a view
      // concern, so it works while the page refuses input. Forgetting the tap
      // keeps a third one from toggling straight back.
      this.lastTapAt = -Infinity;
      this.host.toggleZoom();
      return;
    }
    this.lastTapAt = now;
    if (!this.host.enabled()) return;
    const point = { x: finger.x, y: finger.y };
    // Hover first: a menu that opens on mouseover is otherwise clicked before it
    // has anything to click.
    this.sendMouse("move", point, "none", 0, 0);
    this.sendMouse("down", point, "left", 1, 1);
    this.sendMouse("up", point, "left", 0, 1);
    this.host.anchor(point.x, point.y);
  }

  private startDrag(): void {
    this.longPress = 0;
    const finger = this.first();
    if (!finger || this.mode !== "undecided" || !this.host.enabled()) return;
    this.mode = "drag";
    this.dragPoint = { x: finger.x, y: finger.y };
    this.sendMouse("move", this.dragPoint, "none", 0, 0);
    this.sendMouse("down", this.dragPoint, "left", 1, 1);
  }

  private sendMouse(
    event: "move" | "down" | "up",
    point: { x: number; y: number },
    button: "none" | "left",
    buttons: number,
    clickCount: number,
  ): void {
    this.host.send({
      type: "mouse",
      event,
      ...this.host.remote(point.x, point.y),
      button,
      buttons,
      modifiers: 0,
      clickCount,
    });
  }

  private startFlick(finger: Finger): void {
    const speed = Math.hypot(finger.vx, finger.vy);
    if (speed < TOUCH_FLICK_MIN_SPEED || !this.host.enabled()) return;
    this.flickV = { x: finger.vx, y: finger.vy };
    this.flickPoint = { x: finger.x, y: finger.y };
    this.flickAt = this.host.now();
    this.tickFlick();
  }

  private tickFlick = (): void => {
    const now = this.host.now();
    // Clamp the step: a backgrounded tab resumes with one enormous dt, which
    // would land as a single page-length jump.
    const dt = Math.min(64, Math.max(1, now - this.flickAt));
    this.flickAt = now;
    this.pan(this.flickV.x * dt, this.flickV.y * dt, this.flickPoint.x, this.flickPoint.y);
    const decay = TOUCH_FLICK_DECAY ** (dt / TOUCH_FLICK_DECAY_REF_MS);
    this.flickV = { x: this.flickV.x * decay, y: this.flickV.y * decay };
    if (Math.hypot(this.flickV.x, this.flickV.y) < TOUCH_FLICK_STOP_SPEED) {
      this.flick = 0;
      return;
    }
    this.flick = this.host.after(TOUCH_FLICK_TICK_MS, this.tickFlick);
  };

  private stopFlick(): void {
    if (this.flick) this.host.clear(this.flick);
    this.flick = 0;
    this.flickV = { x: 0, y: 0 };
  }

  private updatePinch(): void {
    const span = this.span();
    if (this.pinchSpan < TOUCH_PINCH_MIN_SPAN || span < TOUCH_PINCH_MIN_SPAN) {
      this.pinchSpan = span;
      return;
    }
    const factor = span / this.pinchSpan;
    this.pinchFactor = Math.abs(factor - 1) < TOUCH_PINCH_DEADZONE ? 1 : factor;
    const [a, b] = this.pair();
    if (a && b) this.host.preview(this.pinchFactor, (a.x + b.x) / 2, (a.y + b.y) / 2);
  }

  private commitPinch(): void {
    this.host.preview(1, 0, 0);
    const factor = this.pinchFactor;
    this.pinchFactor = 1;
    if (factor !== 1) this.host.zoom(factor);
  }

  private span(): number {
    const [a, b] = this.pair();
    return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : 0;
  }

  private pair(): [Finger | undefined, Finger | undefined] {
    const [a, b] = [...this.fingers.values()];
    return [a, b];
  }

  private first(): Finger | undefined {
    return this.fingers.values().next().value;
  }

  private cancelLongPress(): void {
    if (this.longPress) this.host.clear(this.longPress);
    this.longPress = 0;
  }
}
