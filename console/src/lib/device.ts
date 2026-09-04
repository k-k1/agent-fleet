import { useEffect, useState } from "react";

// The phone breakpoint, matched to the 760px media query in styles.css. Centralized
// so the value lives in one place instead of being re-typed at each matchMedia call.
export const MOBILE_QUERY = "(max-width: 760px)";
export const mobileMatches = (): boolean =>
  typeof window !== "undefined" && window.matchMedia(MOBILE_QUERY).matches;

// useIsMobile tracks the phone breakpoint reactively, re-rendering when the viewport
// crosses it. Shared by WsBar (overflow popover) and PaneHost (split limit).
export function useIsMobile(): boolean {
  const [m, setM] = useState(mobileMatches);
  useEffect(() => {
    const mq = window.matchMedia(MOBILE_QUERY);
    const fn = () => setM(mq.matches);
    mq.addEventListener("change", fn);
    // Resync to the current value when the subscription starts: if the boundary was crossed
    // between the initial render and the effect, the value would otherwise stay stale until the
    // next change event.
    fn();
    return () => mq.removeEventListener("change", fn);
  }, []);
  return m;
}

// coarsePointer reports a touch-primary device (phone / tablet), where focusing an
// input pops the on-screen keyboard. We use it to SUPPRESS auto-focus on view
// switch / attach: switching between terminal and chat to read shouldn't summon the
// keyboard. The user taps the terminal or the composer to focus (and get the
// keyboard) when they actually want to type. Desktop (fine pointer) keeps
// auto-focus, where there's no keyboard to intrude. Evaluated at call time so it
// follows a device/input change (e.g. a detachable keyboard) rather than page load.
export const coarsePointer = (): boolean =>
  typeof window !== "undefined" &&
  Boolean(
    window.matchMedia?.("(pointer: coarse)")?.matches ||
      "ontouchstart" in window ||
      (navigator.maxTouchPoints || 0) > 0,
  );

// isStandalonePWA detects the installed-app display mode (no browser chrome, so no
// native reload button) — iOS Safari exposes this only via navigator.standalone,
// everyone else via the display-mode media query.
export const isStandalonePWA = (): boolean =>
  typeof window !== "undefined" &&
  Boolean(
    window.matchMedia?.("(display-mode: standalone)")?.matches ||
      (navigator as unknown as { standalone?: boolean }).standalone === true,
  );

// isMac drives DISPLAY only — whether a shortcut label shows ⌘/⌥ or Ctrl/Alt. Key
// MATCHING stays platform-agnostic (chords treat Ctrl and ⌘ as one "mod"), so this
// never affects which keys fire, only how they read. Prefers the modern
// userAgentData.platform, falling back to the legacy platform / UA string.
export const isMac = (): boolean =>
  typeof navigator !== "undefined" &&
  /mac|iphone|ipad|ipod/i.test(
    (navigator as unknown as { userAgentData?: { platform?: string } }).userAgentData?.platform ||
      navigator.platform ||
      navigator.userAgent ||
      "",
  );
