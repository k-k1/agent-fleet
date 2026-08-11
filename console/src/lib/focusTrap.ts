// useFocusTrap — keep Tab focus inside an overlay (modal / confirm) and restore it to
// the opener on close (docs: keymap redesign P3). Complements useEscLayer (Esc close)
// and useBackClose (device back). Keyboard-only users can't fall out of a dialog into
// the page behind it, and closing returns them to where they were.
//
// Initial focus is DESKTOP-ONLY: on touch, focusing would pop the soft keyboard, so we
// leave it to the user's tap (mirrors ui/Modal's existing coarse-pointer blur).
import { useEffect } from "react";
import type { RefObject } from "react";
import { coarsePointer } from "./device.ts";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusable(root: HTMLElement): HTMLElement[] {
  // offsetParent is null for display:none subtrees — filter those out so Tab doesn't
  // land on a hidden control. (position:fixed elements report null too, but dialog
  // contents aren't fixed.)
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  );
}

export function useFocusTrap(ref: RefObject<HTMLElement | null>, active = true): void {
  useEffect(() => {
    if (!active) return;
    const container = ref.current;
    if (!container) return;
    const opener = document.activeElement as HTMLElement | null;

    // Give the dialog focus so Tab has somewhere to cycle from — unless a child
    // already grabbed it (autoFocus) or we're on touch. A `[data-autofocus]`
    // descendant (e.g. a confirm dialog's primary action) wins over the plain
    // first-focusable fallback, so Space/Enter can fire it without Tabbing.
    if (!coarsePointer() && !container.contains(document.activeElement)) {
      const initial = container.querySelector<HTMLElement>("[data-autofocus]:not([disabled])");
      (initial ?? focusable(container)[0] ?? container).focus?.();
    }

    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const items = focusable(container);
      if (items.length === 0) {
        e.preventDefault();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const el = document.activeElement;
      if (e.shiftKey) {
        if (el === first || !container.contains(el)) {
          e.preventDefault();
          last.focus();
        }
      } else if (el === last || !container.contains(el)) {
        e.preventDefault();
        first.focus();
      }
    };

    container.addEventListener("keydown", onKey);
    return () => {
      container.removeEventListener("keydown", onKey);
      // Return focus to whoever opened the dialog, if it's still around.
      if (opener && document.contains(opener) && !coarsePointer()) opener.focus?.();
    };
  }, [active, ref]);
}
