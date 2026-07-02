// coarsePointer reports a touch-primary device (phone / tablet), where focusing an
// input pops the on-screen keyboard. We use it to SUPPRESS auto-focus on view
// switch / attach: switching ターミナル⇄チャット to read shouldn't summon the
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
