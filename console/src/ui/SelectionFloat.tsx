// SelectionFloat — where a selection-driven group of controls is placed.
//
// Fine pointer: floating beside the selection, clamped into the viewport (unchanged).
//
// Coarse pointer: docked to the bottom edge instead. This is not a styling preference. The
// browser's own text-selection menu (Copy / Share / Select all on Android, the callout on iOS)
// is native UI painted above all web content — no z-index reaches it — and it is anchored
// directly ABOVE the selection, which is exactly where these controls were placed. On a phone it
// spans most of the width, so there is no room to dodge sideways; the only band it never claims
// is the bottom edge, because a selection low enough to push it there makes it flip above the
// selection instead. Reported from a real Android phone: the mark colours were completely hidden
// behind that menu.
//
// None of this is observable in an automated test — the native menu is not in the DOM and
// headless Chromium does not draw it at all — so what is pinned in tests is only the placement
// decision; the rest only a real phone can answer (ADR 0050, addendum 2026-09-05).

import { useLayoutEffect, useRef, type ReactNode } from "react";
import { primaryCoarsePointer } from "../lib/device.ts";
import { placeFixed } from "../lib/placeFixed.ts";
import { holdSelection } from "../lib/selectionCapture.ts";
import { cx } from "./cx.ts";

export interface SelectionFloatProps {
  /** Viewport coordinates of the selection, already lifted by the caller's offset. Ignored when
   *  docked. */
  x: number;
  y: number;
  /** The caller's own group class (.sel-pill-group, .tmark-pill …), kept for its inner layout. */
  className?: string;
  role?: string;
  "aria-label"?: string;
  children: ReactNode;
}

export function SelectionFloat({ x, y, className, role, "aria-label": ariaLabel, children }: SelectionFloatProps) {
  const ref = useRef<HTMLDivElement>(null);
  const docked = primaryCoarsePointer();

  // Floating elements are measured after they render and then nudged into place, so a selection
  // at the right or bottom edge cannot push the controls off screen. Re-run on resize while
  // open: splitting a pane or rotating a phone must not leave them unreachable.
  useLayoutEffect(() => {
    if (docked) return;
    const fit = () => {
      if (ref.current) placeFixed(ref.current, x, y);
    };
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [docked, x, y]);

  return (
    <div
      ref={ref}
      className={cx("sel-float", docked && "sel-float-docked", className)}
      role={role}
      aria-label={ariaLabel}
      // Suspends selection capture for the length of the press. Without it, the tap collapses the
      // selection and the capture that follows removes this element from under the finger before
      // the click lands (lib/selectionCapture).
      onPointerDown={holdSelection}
    >
      {children}
    </div>
  );
}
