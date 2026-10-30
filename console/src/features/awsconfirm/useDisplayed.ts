import { useEffect, useState, type RefObject } from "react";

/** displayed reports whether el is laid out: neither it nor an ancestor has display:none. A
 *  phone keeps the panes of its other columns mounted but hidden that way (PaneHost). */
export function displayed(el: Element | null): boolean {
  for (let e: Element | null = el; e; e = e.parentElement) {
    if (getComputedStyle(e).display === "none") return false;
  }
  return !!el;
}

// How often the check runs when nothing reports a change: a column switch on a phone changes
// an ancestor's style, which neither a resize nor this element's own observer always sees.
const RECHECK_MS = 1500;

/** useDisplayed tracks displayed(ref.current) for a mirror that must know whether the member
 *  can see it (ADR 0126: its session's confirmation requests show there, or as a toast). */
export function useDisplayed(ref: RefObject<Element | null>): boolean {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const check = () => setShown(displayed(ref.current));
    check();
    const t = window.setInterval(check, RECHECK_MS);
    window.addEventListener("resize", check);
    const ro = typeof ResizeObserver !== "undefined" && ref.current ? new ResizeObserver(check) : null;
    if (ro && ref.current) ro.observe(ref.current);
    return () => {
      clearInterval(t);
      window.removeEventListener("resize", check);
      ro?.disconnect();
    };
  }, [ref]);
  return shown;
}
