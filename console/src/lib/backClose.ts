import { useEffect, useRef } from "react";

// useBackClose — close-on-back for overlays (modals). Mounting pushes a throwaway
// history entry; the device/browser back button (or a back gesture) pops it and
// runs `onClose` instead of navigating away. Closing by any other means (Esc,
// backdrop, a button) consumes that entry on cleanup so history stays balanced.
//
// Like useEscLayer, overlays join a shared stack and only the topmost responds,
// so stacked modals peel one at a time — a single back press closes just the top
// one. `onClose` is read through a ref so a parent re-render with a fresh inline
// callback doesn't re-register the layer; only `active` toggling joins/leaves.
//
// The guard entry carries { afModal: true }; App's drawer popstate logic keys off
// { drawer: true }, so the two histories don't collide.
const stack: object[] = [];

export function useBackClose(onClose: (() => void) | undefined, active = true): void {
  const cb = useRef(onClose);
  cb.current = onClose;
  const on = active && !!onClose;
  useEffect(() => {
    if (!on) return;
    const token = {};
    stack.push(token);
    try {
      history.pushState({ __af: true, afModal: true }, "");
    } catch {}
    let closedByBack = false;
    const onPop = () => {
      // Only the topmost overlay reacts; the browser already popped our entry.
      if (stack[stack.length - 1] !== token) return;
      closedByBack = true;
      cb.current?.();
    };
    window.addEventListener("popstate", onPop);
    return () => {
      const i = stack.lastIndexOf(token);
      if (i >= 0) stack.splice(i, 1);
      window.removeEventListener("popstate", onPop);
      // Closed by UI (Esc / backdrop / button), not by back: consume the guard
      // entry we pushed so we don't leave a dead step in the history.
      if (!closedByBack && history.state && history.state.afModal) {
        try {
          history.back();
        } catch {}
      }
    };
  }, [on]);
}
