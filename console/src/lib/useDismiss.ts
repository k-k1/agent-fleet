import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { useEscLayer } from "./escLayer.ts";

// useDismiss: while `open`, close on a mousedown outside `ref` OR an Escape key —
// the shared dismissal for anchored popovers and menus (account menu, appearance popover,
// WsBar resource/usage/preview chips, and the left-pane overflow / launch / add menus).
//
// A containment check (not stopPropagation on the wrapper) is deliberate: it lets
// opening one menu close the others through their own listeners, instead of one menu
// swallowing the document-level close handlers and leaving several open at once.
//
// onClose is read through a ref so the effect only re-subscribes when `open` toggles,
// not on every render (callers can pass an inline `() => setOpen(false)` safely).
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
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      const current = Array.isArray(refs.current) ? refs.current : [refs.current];
      if (!current.some((r) => r.current && r.current.contains(target))) cb.current();
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
}
