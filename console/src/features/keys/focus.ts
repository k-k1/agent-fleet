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
      document.querySelector<HTMLElement>(".pane.pane--active") ?? document.querySelector<HTMLElement>(".panehost .pane");
    (active?.querySelector<HTMLElement>(CONTENT_SELECTOR) ?? active)?.focus?.();
    return;
  }
  if (region === "rail") {
    focusRail();
    return;
  }
  document.querySelector<HTMLElement>(".wsbar")?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
}

// The rail keeps every view mounted and shows one, so focus has to land in that one. With the
// pane folded away there is no view to land in, and the activity bar is the part of the rail
// still on screen: its pressed icon, or its first one.
function focusRail(): void {
  const view = document.querySelector<HTMLElement>(".app-rail .rail-view:not([hidden])");
  const pane = view?.closest<HTMLElement>(".app-rail");
  const folded = !!pane && getComputedStyle(pane).display === "none";
  const inView = view && !folded ? view.querySelector<HTMLElement>(FOCUSABLE_SELECTOR) : null;
  const target =
    inView ??
    document.querySelector<HTMLElement>('.actbar .actbar-btn[aria-pressed="true"]') ??
    document.querySelector<HTMLElement>(".actbar .actbar-btn");
  target?.focus();
}

/** Focus (and select) the left rail's quick-filter box — ProjectTree's search input,
 * which narrows repos and sessions. Deliberately NOT coarsePointer-guarded: unlike
 * navigation focus, this command exists to type into that box, and a tablet with a
 * keyboard still reports a coarse pointer. Called on the frame after the rail is
 * ensured open, so the input exists by then; a no-op if it doesn't. */
export function focusRailFilter(): void {
  const input = document.querySelector<HTMLInputElement>(".proj-filter input");
  if (!input) return;
  input.focus();
  input.select();
}
