// Folding the desktop WS bar by the width it actually has (#1642).
//
// What the bar has to hold varies far more than the window does: one to five usage chips
// (more when pinned or near a cap), AWS / Google Cloud chips, the restart and slot-move
// pills, the image button, the locale's label lengths. A width breakpoint cannot be right
// for all of them, and the old bar let the pane buttons absorb the shortage — a CJK label
// can break after any character, so each button shrank into a one-character column and
// the bar grew three rows tall. Every item now keeps its natural width, and the bar steps
// through these folds, cheapest first, until its content fits:
//
//   LABELS  pane buttons go icon-only ("Start" keeps its label: it is the primary action)
//   MORE    resources, preview, AWS and Google Cloud move behind one ⋯
//   (usage) the usage chips fold into their +N group, keeping only pinned and near-cap ones
//   TIGHT   "Start" goes icon-only too, the caption shrinks to "WS", the state ellipsizes
//
// The CSS steps are attributes on the bar, set here directly rather than through React
// state: each candidate is applied and measured inside one layout pass, so the bar settles
// before it is painted and never flickers between two states. The usage fold is the one
// step React has to render (the chips portal into the +N popover), so it is a state, and
// unfolding it is judged from the width folding it saved last time.

import { useCallback, useLayoutEffect, useRef, useState, type RefObject } from "react";

export const STEP_NONE = 0;
export const STEP_LABELS = 1;
export const STEP_MORE = 2;
export const STEP_TIGHT = 3;
export type FoldStep = typeof STEP_NONE | typeof STEP_LABELS | typeof STEP_MORE | typeof STEP_TIGHT;

// Slack required before the usage chips come back out. Their saving is a remembered number,
// not a measurement of the current content, so unfolding at the exact boundary would refold
// on the next pixel of change.
export const UNFOLD_SLACK = 16;

export interface FoldPlan {
  step: FoldStep;
  foldUsage: boolean;
}

/**
 * planFold picks the cheapest fold that fits. `measure(step)` is the width the bar's content
 * needs at that CSS step with the usage chips as they are now; `saving` is how much folding
 * the usage chips freed when they were last folded (only read while they are folded).
 */
export function planFold(
  measure: (step: FoldStep) => number,
  avail: number,
  usage: { folded: boolean; saving: number },
): FoldPlan {
  const css: FoldStep[] = [STEP_NONE, STEP_LABELS, STEP_MORE];
  if (usage.folded) {
    // The usage fold comes after MORE, so chips that would fit back at any earlier step win.
    for (const s of css) if (measure(s) + usage.saving + UNFOLD_SLACK <= avail) return { step: s, foldUsage: false };
    for (const s of css) if (measure(s) <= avail) return { step: s, foldUsage: true };
    return { step: STEP_TIGHT, foldUsage: true };
  }
  for (const s of css) if (measure(s) <= avail) return { step: s, foldUsage: false };
  // Fold the chips and measure again once React has moved them; TIGHT is only reached if
  // that is still not enough.
  return { step: STEP_MORE, foldUsage: true };
}

export function applyFoldStep(bar: HTMLElement, step: FoldStep) {
  bar.toggleAttribute("data-fold-labels", step >= STEP_LABELS);
  bar.toggleAttribute("data-fold-more", step >= STEP_MORE);
  bar.toggleAttribute("data-fold-tight", step >= STEP_TIGHT);
}

function clearFold(bar: HTMLElement) {
  bar.removeAttribute("data-fold-labels");
  bar.removeAttribute("data-fold-more");
  bar.removeAttribute("data-fold-tight");
}

/**
 * barContentWidth: what the bar's in-flow children need side by side. Not scrollWidth: that
 * also counts an open popover hanging past the bar's edge, which would fold the bar for as
 * long as the popover is open. Relies on the children not shrinking (wsbar.css sets
 * flex-shrink: 0 on desktop) — a shrunk child would always "fit".
 */
export function barContentWidth(bar: HTMLElement): number {
  const cs = getComputedStyle(bar);
  const gap = parseFloat(cs.columnGap) || 0;
  let width = (parseFloat(cs.paddingLeft) || 0) + (parseFloat(cs.paddingRight) || 0);
  let items = 0;
  for (const el of Array.from(bar.children)) {
    const s = getComputedStyle(el);
    if (s.display === "none" || s.position === "absolute" || s.position === "fixed") continue;
    // The spacer is a flex item (it takes a gap on each side) but asks for no width itself.
    if (!el.classList.contains("ws-spacer")) width += el.getBoundingClientRect().width;
    items++;
  }
  return width + gap * Math.max(0, items - 1);
}

/**
 * useWsBarFold keeps the bar folded to its width while `enabled` (desktop). `measureWidth`
 * is barContentWidth outside tests. Returns the two folds the rest of WsBar has to know
 * about: whether the usage chips are folded, and whether the ⋯ holds the right-hand items.
 */
export function useWsBarFold(
  barRef: RefObject<HTMLElement | null>,
  enabled: boolean,
  measureWidth: (bar: HTMLElement) => number = barContentWidth,
): { foldUsage: boolean; foldMore: boolean } {
  const [foldUsage, setFoldUsage] = useState(false);
  const [foldMore, setFoldMore] = useState(false);
  const usageRef = useRef(foldUsage);
  usageRef.current = foldUsage;
  // saving: width the last usage fold freed. pending: the unfolded width at MORE, recorded
  // when the fold is asked for and turned into `saving` once React has rendered it.
  const mem = useRef<{ saving: number; pending: number | null }>({ saving: 0, pending: null });

  const settle = useCallback(() => {
    const bar = barRef.current;
    if (!bar) return;
    const avail = bar.clientWidth;
    const seen = new Map<FoldStep, number>();
    const measure = (step: FoldStep) => {
      let w = seen.get(step);
      if (w === undefined) {
        applyFoldStep(bar, step);
        w = measureWidth(bar);
        seen.set(step, w);
      }
      return w;
    };
    const m = mem.current;
    if (usageRef.current && m.pending !== null) {
      m.saving = Math.max(0, m.pending - measure(STEP_MORE));
      m.pending = null;
    }
    const plan = planFold(measure, avail, { folded: usageRef.current, saving: m.saving });
    if (plan.foldUsage && !usageRef.current) m.pending = measure(STEP_MORE);
    applyFoldStep(bar, plan.step);
    if (plan.foldUsage !== usageRef.current) setFoldUsage(plan.foldUsage);
    setFoldMore(plan.step >= STEP_MORE);
  }, [barRef, measureWidth]);

  // Re-settle after every render that changes the usage fold (the chips have just moved),
  // and whenever desktop mode flips.
  useLayoutEffect(() => {
    const bar = barRef.current;
    if (!bar) return;
    if (!enabled) {
      clearFold(bar);
      mem.current = { saving: 0, pending: null };
      setFoldUsage(false);
      setFoldMore(false);
      return;
    }
    settle();
  }, [barRef, enabled, foldUsage, settle]);

  // Width changes come from the window (the bar) and from the content: a chip appearing,
  // a reading growing a digit, the locale, a late web font. Watching every child catches
  // all of them; the spacer alone would miss changes while the bar is already full.
  useLayoutEffect(() => {
    const bar = barRef.current;
    if (!bar || !enabled || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => settle());
    const watch = () => {
      ro.disconnect();
      ro.observe(bar);
      for (const el of Array.from(bar.children)) ro.observe(el);
    };
    watch();
    const mo = new MutationObserver(() => {
      watch();
      settle();
    });
    mo.observe(bar, { childList: true });
    return () => {
      ro.disconnect();
      mo.disconnect();
    };
  }, [barRef, enabled, settle]);

  return { foldUsage: enabled && foldUsage, foldMore: enabled && foldMore };
}
