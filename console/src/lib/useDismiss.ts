import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { useEscLayer } from "./escLayer.ts";

// useDismiss: while `open`, close on a mousedown outside `ref` OR an Escape key —
// the shared dismissal for anchored popovers and menus (account menu, 外観 popover,
// WsBar resource/usage/preview chips, and the left-pane ⋯ / 起動 / ＋ menus).
//
// A containment check (not stopPropagation on the wrapper) is deliberate: it lets
// opening one menu close the others through their own listeners, instead of one menu
// swallowing the document-level close handlers and leaving several open at once.
//
// onClose is read through a ref so the effect only re-subscribes when `open` toggles,
// not on every render (callers can pass an inline `() => setOpen(false)` safely).
export function useDismiss<T extends HTMLElement>(
  ref: RefObject<T | null>,
  open: boolean,
  onClose: () => void,
): void {
  const cb = useRef(onClose);
  cb.current = onClose;
  // Escape goes through the shared layer stack so a popover open above a modal
  // closes alone — the modal's own Esc handler stays quiet until the next press.
  useEscLayer(() => cb.current(), open);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) cb.current();
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [ref, open]);
}
