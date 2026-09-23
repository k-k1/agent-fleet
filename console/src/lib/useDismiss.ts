import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { useEscLayer } from "./escLayer.ts";

// useDismiss: while `open`, close on a press outside `ref` OR an Escape key — the shared
// dismissal for anchored popovers and menus (account menu, appearance popover, WsBar chips,
// the left-pane overflow / launch / add menus, the right-click menus).
//
// A primary press (click / tap) that lands outside EVERY open popover only closes them: the
// whole gesture — pointerdown, mousedown, mouseup, click — is swallowed before the page sees
// it, so the button, row or terminal under the finger does not also fire. Without this, a tap
// meant to put a menu away also sends a message, opens a session or starts a text selection.
// A press inside one open popover still closes the others without being swallowed, so
// opening a submenu or clicking into a sibling popover keeps working. A secondary press is
// never swallowed: right-clicking elsewhere closes this menu and opens that spot's own menu.
//
// onClose is read through a ref so the layer only re-registers when `open` toggles, not on
// every render (callers can pass an inline `() => setOpen(false)` safely).
export function useDismiss(
  ref: RefObject<HTMLElement | null> | Array<RefObject<HTMLElement | null>>,
  open: boolean,
  onClose: () => void,
): void {
  const cb = useRef(onClose);
  const refs = useRef(ref);
  cb.current = onClose;
  refs.current = ref;
  // Escape goes through the shared layer stack so a popover open above a modal
  // closes alone — the modal's own Esc handler stays quiet until the next press.
  useEscLayer(() => cb.current(), open);
  useEffect(() => {
    if (!open) return;
    const layer: Layer = {
      contains: (target) => {
        const current = Array.isArray(refs.current) ? refs.current : [refs.current];
        return current.some((r) => !!r.current && r.current.contains(target));
      },
      close: () => cb.current(),
    };
    return addLayer(layer);
  }, [open]);
}

interface Layer {
  contains: (target: Node) => boolean;
  close: () => void;
}

const layers: Layer[] = [];

// How long after the pointer lifts the swallowed gesture's click may still arrive. Touch
// browsers can hold the click back ~300 ms; the window also bounds how long a gesture whose
// click never comes (released off the page) can eat an unrelated one.
const CLICK_GRACE_MS = 800;

// The state of the gesture in flight. `sawPointerDown` tells the mousedown that follows a
// pointerdown apart from a lone mousedown (a synthetic event, or a browser without pointer
// events), which has to act as the press itself.
let swallowing = false;
let sawPointerDown = false;
let graceTimer: number | undefined;

function stopSwallowing(): void {
  swallowing = false;
  window.clearTimeout(graceTimer);
  graceTimer = undefined;
}

function eat(e: Event): void {
  e.preventDefault();
  e.stopImmediatePropagation();
}

// press closes every open layer the target is outside of, and reports whether the press
// landed outside all of them — the case where the gesture must not reach the page.
function press(target: Node | null): boolean {
  if (!target || layers.length === 0) return false;
  const inside = layers.some((l) => l.contains(target));
  for (const l of layers.slice()) if (!l.contains(target)) l.close();
  return !inside;
}

const onPointerDown = (e: PointerEvent) => {
  stopSwallowing();
  sawPointerDown = true;
  if (e.button !== 0 || !e.isPrimary) {
    press(e.target as Node | null);
    return;
  }
  if (press(e.target as Node | null)) {
    swallowing = true;
    eat(e);
  }
};

const onMouseDown = (e: MouseEvent) => {
  if (sawPointerDown) {
    sawPointerDown = false;
    if (swallowing) eat(e);
    return;
  }
  stopSwallowing();
  if (e.button !== 0) {
    press(e.target as Node | null);
    return;
  }
  if (press(e.target as Node | null)) {
    swallowing = true;
    eat(e);
  }
};

const onRelease = (e: Event) => {
  if (!swallowing) return;
  eat(e);
  window.clearTimeout(graceTimer);
  graceTimer = window.setTimeout(stopSwallowing, CLICK_GRACE_MS);
};

// Cancelling touchend also stops the browser from synthesising the tap's mouse events and click.
const onTouchEnd = (e: Event) => {
  if (swallowing) eat(e);
};

const onClick = (e: MouseEvent) => {
  // A click ends the gesture; a pointerdown whose mousedown never came must not make the
  // next lone mousedown look like its follower.
  sawPointerDown = false;
  // detail 0 is a keyboard- or script-issued click, never the tail of the pointer gesture.
  if (!swallowing || e.detail === 0) return;
  eat(e);
  stopSwallowing();
};

const onCancel = () => {
  sawPointerDown = false;
  stopSwallowing();
};

// Listeners go on `window` in the capture phase: that runs before React's root listeners and
// before any element's own handler, which is the only place a gesture can still be withheld.
const LISTENERS: Array<[string, (e: never) => void]> = [
  ["pointerdown", onPointerDown],
  ["mousedown", onMouseDown],
  ["pointerup", onRelease],
  ["mouseup", onRelease],
  ["touchend", onTouchEnd],
  ["click", onClick],
  ["pointercancel", onCancel],
];

// Attached once, on the first popover, and left in place: every handler is a no-op while
// nothing is open, and a gesture being swallowed must still be caught after the popover it
// closed has unmounted.
let attached = false;

function addLayer(layer: Layer): () => void {
  if (!attached) {
    attached = true;
    for (const [type, fn] of LISTENERS) window.addEventListener(type, fn as EventListener, { capture: true, passive: false });
  }
  layers.push(layer);
  return () => {
    const i = layers.indexOf(layer);
    if (i >= 0) layers.splice(i, 1);
  };
}
