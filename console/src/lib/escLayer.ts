import { useEffect, useRef } from "react";

// useEscLayer — close-on-Escape for stacked overlays (modals, confirm dialogs,
// menus). Each open overlay joins a global stack; Escape triggers only the
// topmost one, so stacked surfaces peel one at a time. Without this, every
// overlay's document-level keydown fires on the same keypress — a confirm asked
// from a modal and the modal beneath it would both close at once.
//
// Stack order is join order, which matches visual stacking: an overlay opened
// later renders above. `onEsc` is read through a ref so a parent re-render with
// a fresh inline callback doesn't re-register the layer (and jump it to the top
// of the stack); only `active` toggling joins/leaves.
const stack: object[] = [];

export function useEscLayer(onEsc: (() => void) | undefined, active = true): void {
  const cb = useRef(onEsc);
  cb.current = onEsc;
  const on = active && !!onEsc;
  useEffect(() => {
    if (!on) return;
    const token = {};
    stack.push(token);
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (stack[stack.length - 1] !== token) return;
      cb.current?.();
    };
    document.addEventListener("keydown", onKey);
    return () => {
      const i = stack.lastIndexOf(token);
      if (i >= 0) stack.splice(i, 1);
      document.removeEventListener("keydown", onKey);
    };
  }, [on]);
}
