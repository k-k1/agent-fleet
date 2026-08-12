// DOM focus helpers for the keyboard system (P1). Moving keyboard focus is the ONE
// place navigation must touch the DOM: pane activation is driven by the layout store
// (activeId) and its content effects, but region cycling (rail ⇄ main ⇄ bars) and
// focusing a non-input pane view (file/scm — no autofocus effect) need an explicit
// .focus(). Kept out of commands.ts (data) and dispatcher.ts (event routing).
import { coarsePointer } from "../../lib/device.ts";
import type { Region } from "../../lib/keys/registry.ts";

// Content targets INSIDE a pane, in preference order. Excludes <button> so a pane's
// grip / control buttons (which precede the content in DOM order) are never focused —
// we want the terminal textarea, a composer, or a focusable view (CodeView / SCM
// graph carry tabindex=0), not the chrome.
const CONTENT_SELECTOR = ".xterm-helper-textarea, .view textarea, .view input, textarea, input, [tabindex]:not([tabindex='-1'])";
const FOCUSABLE_SELECTOR =
  'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function cssEscape(s: string): string {
  return typeof CSS !== "undefined" && CSS.escape ? CSS.escape(s) : s.replace(/["\\]/g, "\\$&");
}

/** Focus the content of a specific pane (terminal textarea / composer / focusable
 * view). No-op on touch devices, where focusing would summon the soft keyboard. */
export function focusPaneContent(paneId: string): void {
  if (coarsePointer()) return;
  const pane = document.querySelector<HTMLElement>(`.pane[data-cell-id="${cssEscape(paneId)}"]`);
  pane?.querySelector<HTMLElement>(CONTENT_SELECTOR)?.focus();
}

/** Move keyboard focus into a screen region. main → the active pane's content; rail /
 * bars → their first focusable control. */
export function focusRegion(region: Region): void {
  if (coarsePointer()) return;
  if (region === "main") {
    const active =
      document.querySelector<HTMLElement>(".pane.active") ?? document.querySelector<HTMLElement>(".panehost .pane");
    (active?.querySelector<HTMLElement>(CONTENT_SELECTOR) ?? active)?.focus?.();
    return;
  }
  const root = document.querySelector<HTMLElement>(region === "rail" ? ".app-rail" : ".wsbar");
  root?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
}
