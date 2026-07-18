export type BrowserPageState = "loading" | "ready" | "disconnected" | "crashed" | "target-unreachable";
export type BrowserMouseButton = "none" | "left" | "middle" | "right";

export interface BrowserConsoleEntry {
  level: string;
  text: string;
  ts: string;
}

export type BrowserOutbound =
  | { type: "viewport"; width: number; height: number }
  | { type: "mouse"; event: "move" | "down" | "up"; x: number; y: number; button: BrowserMouseButton; buttons: number; modifiers: number; clickCount: number }
  | { type: "wheel"; x: number; y: number; deltaX: number; deltaY: number; modifiers: number }
  | { type: "key"; event: "down" | "up"; key: string; code: string; modifiers: number; repeat: boolean }
  | { type: "text"; text: string }
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
    if (this.composing || e.isComposing) {
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
