// The gateway chords (Ctrl/⌘+P for the palette, Ctrl/⌘+K for the leader) must never fall
// through to the browser's own action, even where the dispatcher declines to act on them.
// The bug this pins: with the "start work" modal open, Ctrl+P opened the browser's print
// dialog, because the dispatcher went inert behind the overlay without cancelling the
// default. The same hole existed on auto-repeat, during IME composition and when a
// terminal is yielded the chord.
//
// preventDefault is the browser-visible half; the other half is that the key still reaches
// the overlay / terminal beneath (no stopPropagation), which the listener on document checks.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let mac = false;
vi.mock("../../lib/device.ts", async (orig) => ({
  ...(await orig<typeof import("../../lib/device.ts")>()),
  isMac: () => mac,
}));

import { useEscLayer } from "../../lib/escLayer.ts";
import { setSetting, defaultSetting } from "../../lib/settings.ts";
import { wireKeys } from "./dispatcher.ts";
import { useKeysStore } from "./store.ts";
import { setBinding, APP_PALETTE } from "./bindings.ts";

interface KeyOpts {
  ctrl?: boolean;
  meta?: boolean;
  repeat?: boolean;
  composing?: boolean;
}

/** Dispatches a real keydown at the focused element (or body). Returns whether the browser's
 *  default was cancelled and whether the key still reached a listener on document. */
const press = (code: string, o: KeyOpts = {}): { prevented: boolean; reached: boolean } => {
  let reached = false;
  const seen = () => (reached = true);
  document.addEventListener("keydown", seen);
  const e = new KeyboardEvent("keydown", {
    code,
    key: code.replace(/^Key/, "").toLowerCase(),
    ctrlKey: !!o.ctrl,
    metaKey: !!o.meta,
    repeat: !!o.repeat,
    isComposing: !!o.composing,
    bubbles: true,
    cancelable: true,
  });
  (document.activeElement ?? document.body).dispatchEvent(e);
  document.removeEventListener("keydown", seen);
  return { prevented: e.defaultPrevented, reached };
};

function Overlay() {
  useEscLayer(() => {});
  return <div className="modal" />;
}

describe("gateway chords never reach the browser's own action", () => {
  let unwire: (() => void) | null = null;
  let host: HTMLDivElement | null = null;
  let root: Root | null = null;

  const openOverlay = () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<Overlay />));
  };

  beforeEach(() => {
    mac = false;
    useKeysStore.setState({ paletteOpen: false, leaderPending: false, leaderPath: [], whichKeyOpen: false });
    unwire = wireKeys();
  });
  afterEach(() => {
    unwire?.();
    unwire = null;
    if (root) act(() => root!.unmount());
    host?.remove();
    root = null;
    host = null;
    document.body.innerHTML = "";
    setSetting("terminalPriority", defaultSetting("terminalPriority"));
    setBinding(APP_PALETTE, null);
  });

  it("cancels Ctrl+P (print) and Ctrl+K behind an open modal, without opening the palette", () => {
    openOverlay();
    const p = press("KeyP", { ctrl: true });
    expect(p.prevented).toBe(true);
    // The modal beneath still sees the key: only the default is cancelled.
    expect(p.reached).toBe(true);
    expect(useKeysStore.getState().paletteOpen).toBe(false);
    expect(press("KeyK", { ctrl: true }).prevented).toBe(true);
    expect(useKeysStore.getState().leaderPending).toBe(false);
  });

  it("leaves other chords behind a modal to the browser", () => {
    openOverlay();
    expect(press("KeyC", { ctrl: true }).prevented).toBe(false);
    expect(press("KeyP").prevented).toBe(false);
  });

  it("on macOS cancels ⌘P but leaves Ctrl+P / Ctrl+K to the text field (line up / kill line)", () => {
    mac = true;
    openOverlay();
    expect(press("KeyP", { meta: true }).prevented).toBe(true);
    expect(press("KeyP", { ctrl: true }).prevented).toBe(false);
    expect(press("KeyK", { ctrl: true }).prevented).toBe(false);
  });

  it("cancels a held-down Ctrl+P's auto-repeats", () => {
    expect(press("KeyP", { ctrl: true, repeat: true }).prevented).toBe(true);
  });

  it("cancels Ctrl+P while an IME is composing, without taking the key from the IME", () => {
    const r = press("KeyP", { ctrl: true, composing: true });
    expect(r.prevented).toBe(true);
    expect(r.reached).toBe(true);
    expect(useKeysStore.getState().paletteOpen).toBe(false);
  });

  it("cancels ⌘P that a focused terminal is yielded under terminal priority (xterm grabs only Ctrl)", () => {
    mac = true;
    setSetting("terminalPriority", true);
    const term = document.createElement("div");
    term.className = "xterm";
    const ta = document.createElement("textarea");
    term.appendChild(ta);
    document.body.appendChild(term);
    ta.focus();
    const r = press("KeyP", { meta: true });
    expect(r.prevented).toBe(true);
    expect(r.reached).toBe(true);
    expect(useKeysStore.getState().paletteOpen).toBe(false);
  });

  it("still opens the palette on Ctrl+P when nothing is in the way", () => {
    expect(press("KeyP", { ctrl: true }).prevented).toBe(true);
    expect(useKeysStore.getState().paletteOpen).toBe(true);
  });

  it("gives Ctrl+P back to the browser once the palette is rebound elsewhere", () => {
    setBinding(APP_PALETTE, "mod+shift+p");
    openOverlay();
    expect(press("KeyP", { ctrl: true }).prevented).toBe(false);
  });
});
