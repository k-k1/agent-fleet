export type BrowserPageState = "loading" | "ready" | "disconnected" | "crashed" | "target-unreachable";
export type BrowserMouseButton = "none" | "left" | "middle" | "right";

export interface BrowserConsoleEntry {
  level: string;
  text: string;
  ts: string;
}

export type BrowserOutbound =
  // zoom is the pinch zoom applied ON TOP of the pane (and of `fit`): the Agent
  // lays the page out that much smaller and renders at a matching device pixel
  // ratio, so the pane shows fewer CSS pixels at full sharpness. 1 = unzoomed.
  | { type: "viewport"; width: number; height: number; fit?: boolean; zoom?: number }
  | { type: "mouse"; event: "move" | "down" | "up"; x: number; y: number; button: BrowserMouseButton; buttons: number; modifiers: number; clickCount: number }
  | { type: "wheel"; x: number; y: number; deltaX: number; deltaY: number; modifiers: number }
  | { type: "key"; event: "down" | "up"; key: string; code: string; modifiers: number; repeat: boolean }
  | { type: "text"; text: string }
  | { type: "copy" }
  | { type: "navigate"; path: string }
  | { type: "reload"; ignoreCache: boolean }
  | { type: "history"; direction: "back" | "forward" }
  | { type: "visibility"; visible: boolean };

export interface BrowserSnapshot {
  state: BrowserPageState;
  url: string;
  title: string;
  width: number;
  height: number;
  canBack: boolean;
  canForward: boolean;
  errorCode: string;
  errorMessage: string;
  console: readonly BrowserConsoleEntry[];
}

/**
 * Pinch zoom bounds. 1 is the pane's own layout (or the fitted one) and zooming
 * OUT past it is deliberately not offered: pinching back to 1 is then the reset
 * gesture, so a zoomed pane can never be left in a state with no way back.
 * Mirrors browserMaxZoom in the Agent, which clamps again.
 */
export const BROWSER_MAX_ZOOM = 4;

export function clampZoom(zoom: number): number {
  if (!Number.isFinite(zoom)) return 1;
  // Two decimals: a pinch produces a continuous ratio, and sending every
  // hundredth of it would relayout the remote page for nothing.
  return Math.min(BROWSER_MAX_ZOOM, Math.max(1, Math.round(zoom * 100) / 100));
}

export interface ModifierLike {
  altKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
}

/** CDP Input modifier mask: Alt=1, Ctrl=2, Meta=4, Shift=8. */
export function modifiersOf(e: ModifierLike): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

export interface PointLike extends ModifierLike {
  clientX: number;
  clientY: number;
}

export interface RectLike {
  left: number;
  top: number;
  width: number;
  height: number;
}

export function remotePoint(e: PointLike, rect: RectLike, width: number, height: number): { x: number; y: number } {
  const x = rect.width > 0 ? ((e.clientX - rect.left) * width) / rect.width : 0;
  const y = rect.height > 0 ? ((e.clientY - rect.top) * height) / rect.height : 0;
  return {
    x: Math.max(0, Math.min(width, x)),
    y: Math.max(0, Math.min(height, y)),
  };
}

export function mouseButton(button: number): BrowserMouseButton {
  return button === 0 ? "left" : button === 1 ? "middle" : button === 2 ? "right" : "none";
}

/**
 * The button currently held down, from a PointerEvent's `buttons` bitmask
 * (1=left, 2=right, 4=middle). A move sent as button "none" is a plain hover as
 * far as Blink is concerned, so every drag — scrollbar thumb, text selection,
 * slider, drag & drop — silently did nothing. Measured against a real Chromium:
 * with "none" scrollX stayed 0, with the held button it scrolled.
 */
export function heldButton(buttons: number): BrowserMouseButton {
  if (buttons & 1) return "left";
  if (buttons & 2) return "right";
  if (buttons & 4) return "middle";
  return "none";
}

/**
 * Wheel deltas in CSS pixels. A WheelEvent may report lines (deltaMode 1) or
 * pages (2) — a mouse wheel on Windows/Firefox commonly reports 3 LINES, and
 * forwarding that raw makes the remote page scroll 3 pixels, which reads as
 * "the wheel does nothing".
 */
export function wheelPixels(deltaX: number, deltaY: number, deltaMode: number, viewportHeight: number): { deltaX: number; deltaY: number } {
  const factor = deltaMode === 1 ? 16 : deltaMode === 2 ? Math.max(1, viewportHeight) : 1;
  return { deltaX: deltaX * factor, deltaY: deltaY * factor };
}

/** True for the clipboard shortcuts we must NOT swallow as remote key events. */
export function clipboardShortcut(e: RemoteKeyLike): "copy" | "paste" | "cut" | null {
  if (!(e.ctrlKey || e.metaKey) || e.altKey) return null;
  const key = e.key.toLowerCase();
  if (key === "c") return "copy";
  if (key === "v") return "paste";
  if (key === "x") return "cut";
  return null;
}

export interface RemoteKeyLike extends ModifierLike {
  key: string;
  code: string;
  repeat: boolean;
  isComposing?: boolean;
}

/** Keeps IME composition out of raw key events and emits only committed text. */
export class BrowserInputBridge {
  private composing = false;
  private readonly composingCodes = new Set<string>();
  private committedInput = "";

  constructor(private readonly send: (message: BrowserOutbound) => void) {}

  compositionStart(): void {
    this.composing = true;
  }

  compositionEnd(text: string): void {
    this.composing = false;
    if (text) {
      this.committedInput = text;
      this.send({ type: "text", text });
    }
  }

  input(text: string): void {
    if (!text || this.composing) return;
    if (text === this.committedInput) {
      this.committedInput = "";
      return;
    }
    this.send({ type: "text", text });
  }

  keyDown(e: RemoteKeyLike): void {
    // The first keydown that starts an IME arrives before compositionstart, so it comes in
    // with isComposing=false and key="Process". Forwarding it would leave down/up
    // asymmetric, since the matching keyup is swallowed while composing — treat it as
    // composition and just remember the code.
    if (this.composing || e.isComposing || e.key === "Process") {
      if (e.code) this.composingCodes.add(e.code);
      return;
    }
    this.committedInput = "";
    this.send({ type: "key", event: "down", key: e.key, code: e.code, modifiers: modifiersOf(e), repeat: e.repeat });
  }

  keyUp(e: RemoteKeyLike): void {
    if (this.composing || e.isComposing || this.composingCodes.delete(e.code)) return;
    this.send({ type: "key", event: "up", key: e.key, code: e.code, modifiers: modifiersOf(e), repeat: false });
  }
}
