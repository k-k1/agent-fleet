import { useEffect, useRef } from "react";
import type { MutableRefObject } from "react";

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
// The cleanup's own history.back() also fires a popstate — asynchronously, after
// the stack has already been updated. Untracked, that echo was indistinguishable
// from a user back-press and hit whatever layer was on top by then: UI-closing
// the top of a STACKED pair cascaded into the one below, and SWAPPING modals in
// one commit closed the incoming modal right after it opened (launch flow Ph2 hub →
// Start working). `suppress` counts those self-inflicted pops so the shared
// listener swallows exactly that many popstates before treating one as the user's.
//
// The guard entry carries { afModal: true }; App's drawer popstate logic keys off
// { drawer: true }, so the two histories don't collide.
interface Layer {
  cb: MutableRefObject<(() => void) | undefined>;
  closedByBack: boolean;
}

const stack: Layer[] = [];
let suppress = 0;
let wired = false;

function wire(): void {
  if (wired || typeof window === "undefined") return;
  wired = true;
  window.addEventListener("popstate", () => {
    if (suppress > 0) {
      suppress--; // echo of a cleanup's history.back(), not a user back-press
      return;
    }
    const top = stack[stack.length - 1];
    if (!top) return;
    top.closedByBack = true; // the browser already popped this layer's entry
    top.cb.current?.();
  });
}

export function useBackClose(onClose: (() => void) | undefined, active = true): void {
  const cb = useRef(onClose);
  cb.current = onClose;
  const on = active && !!onClose;
  useEffect(() => {
    if (!on) return;
    wire();
    const layer: Layer = { cb, closedByBack: false };
    stack.push(layer);
    try {
      history.pushState({ __af: true, afModal: true }, "");
    } catch {}
    return () => {
      const i = stack.lastIndexOf(layer);
      if (i >= 0) stack.splice(i, 1);
      // Closed by UI (Esc / backdrop / button), not by back: consume the guard
      // entry we pushed so we don't leave a dead step in the history — and mark
      // the resulting popstate as ours (see `suppress` above).
      if (!layer.closedByBack && history.state && history.state.afModal) {
        suppress++;
        try {
          history.back();
        } catch {
          suppress--;
        }
      }
    };
  }, [on]);
}
