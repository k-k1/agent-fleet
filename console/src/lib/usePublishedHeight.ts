import { useCallback } from "react";

/**
 * usePublishedHeight returns a callback ref that keeps `name` on the element's parent equal to
 * the element's rendered height ("<n>px"), so sticky descendants of that parent can offset
 * themselves below it. A fixed pixel guess breaks as soon as the band wraps to a second line
 * (the touch layout wraps the repo row), and the next sticky tier then slides under this one.
 *
 * A callback ref rather than an effect: the measured band often mounts later than its owner
 * (a Section's body only renders while open), and an owner's effect would not see it.
 * With `enabled = false` the variable is left unset, so it inherits or takes the CSS fallback.
 */
export function usePublishedHeight<T extends HTMLElement>(name: string, enabled = true): (el: T | null) => (() => void) | void {
  return useCallback(
    (el: T | null) => {
      const target = el?.parentElement;
      if (!enabled || !el || !target) return;
      const apply = () => target.style.setProperty(name, el.offsetHeight + "px");
      apply();
      const ro = new ResizeObserver(apply);
      ro.observe(el);
      return () => {
        ro.disconnect();
        target.style.removeProperty(name);
      };
    },
    [name, enabled],
  );
}
