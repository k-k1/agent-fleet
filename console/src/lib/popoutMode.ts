// Pop-out tab mode (detaching a pane into its own tab). A tab opened via a pop-out link runs
// in one of two modes:
//   "popout" — minimal chrome: a thin title bar + the single pane, reduced keys.
//   "full"   — the normal console chrome seeded with a 1-pane layout.
// null = a regular console tab. The flag lives in sessionStorage so it is
// per-tab and survives reloads, and this module is dependency-free (no store /
// API imports) because the layout store reads it inside persist() — importing
// anything app-side here would create a cycle.
import { useSyncExternalStore } from "react";

const KEY = "af.popout.mode";
export type PopoutMode = "popout" | "full" | null;

function read(): PopoutMode {
  try {
    const v = sessionStorage.getItem(KEY);
    return v === "popout" || v === "full" ? v : null;
  } catch {
    return null; // SSR / tests / locked-down browsers
  }
}

let mode: PopoutMode = read();
const subs = new Set<() => void>();
const subscribe = (fn: () => void): (() => void) => {
  subs.add(fn);
  return () => subs.delete(fn);
};

export const popoutMode = (): PopoutMode => mode;

/** Set (or clear) this tab's pop-out mode. "popout" → "full" is the title bar's
 * "expand" button (「展開」) converting the tab into a normal console in place. */
export function setPopoutMode(m: PopoutMode): void {
  if (mode === m) return;
  mode = m;
  try {
    if (m) sessionStorage.setItem(KEY, m);
    else sessionStorage.removeItem(KEY);
  } catch {}
  subs.forEach((fn) => fn());
}

/** Reactive mode for components (App shell chrome branch, Pane header button). */
export function usePopoutMode(): PopoutMode {
  return useSyncExternalStore(subscribe, popoutMode);
}
