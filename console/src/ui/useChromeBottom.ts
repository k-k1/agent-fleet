import { useLayoutEffect, useState, type RefObject } from "react";
import { measureChromeBottom, touchesChrome } from "./toastPlacement.ts";

/** useChromeBottom returns where the run of bars at the top of the frame ends (toastPlacement.ts),
 *  or null until it has been measured, for a floating element that must stay off the composer on
 *  a phone. Nothing is measured while `active` is false.
 *
 *  Measured before paint, so the element never flashes at the bottom first. Re-measured when
 *  `remeasureKey` changes, when the viewport moves (a soft keyboard shifts the frame,
 *  app/viewport.ts), and when the page under the element changes while it is up: a sticky toast
 *  outlives a switch of pane or studio tab, which brings in or takes away a header (measured: the
 *  studio's chat tab adds one at 213-249px that a stack placed on the form tab covered). Only
 *  mutations that touch a bar count (touchesChrome), coalesced to one measurement per frame; an
 *  unchanged value does not re-render.
 *
 *  `self` is the element the value places, when it is itself marked data-toast-floor so that the
 *  toasts stay below it: it is left out of its own measurement. */
export function useChromeBottom(active: boolean, remeasureKey?: unknown, self?: RefObject<Element | null>): number | null {
  const [top, setTop] = useState<number | null>(null);
  useLayoutEffect(() => {
    if (!active) return;
    const sync = () => setTop(measureChromeBottom(document, self?.current));
    sync();
    let frame = 0;
    const later = () => {
      if (!frame) frame = requestAnimationFrame(() => ((frame = 0), sync()));
    };
    const mo = new MutationObserver((records) => {
      if (touchesChrome(records)) later();
    });
    // Every attribute, not a list: views show and hide their parts through their own attributes
    // (the studio's tabs through data-tab on .igen-body), and touchesChrome keeps only those on
    // an element that holds a bar.
    mo.observe(document.body, { childList: true, subtree: true, attributes: true });
    const vv = window.visualViewport;
    window.addEventListener("resize", sync);
    vv?.addEventListener("resize", sync);
    vv?.addEventListener("scroll", sync);
    return () => {
      mo.disconnect();
      if (frame) cancelAnimationFrame(frame);
      window.removeEventListener("resize", sync);
      vv?.removeEventListener("resize", sync);
      vv?.removeEventListener("scroll", sync);
    };
  }, [active, remeasureKey, self]);
  return top;
}
