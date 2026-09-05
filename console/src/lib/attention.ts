// Interaction beacon (docs/log/75 P3): tells the Workspace's idle clock that a person is using
// the Console right now.
//
// Why it is needed: idle auto-stop decides presence from terminal keystrokes alone, so that a tab
// left open cannot keep a Workspace warm forever. That makes someone reading back through the
// mirror without typing or sending look absent, and if the container stops mid-read the Agent
// goes with it, so even the transcript becomes unavailable.
//
// What is sent is "a person acted", never "a tab is open". Confusing the two brings back exactly
// what P3 removed: a forgotten open tab billing forever. Hence:
//   - only while the document is visible (never a background tab or a window behind another)
//   - only on real interaction (isTrusted; synthetic, programmatic events do not count)
//   - at most once per 60 seconds (no POST per scroll)
// Counting scroll and click is deliberate the other way round: reading involves no keystrokes.
import { workspaceAttention } from "../core/api/client.ts";

/** Minimum interval between beacons. The CP folds them to 5 s anyway, but pointless round trips
 *  are stopped here. */
export const ATTENTION_INTERVAL_MS = 60_000;

/** Events counted as human interaction. keydown belongs here: keys typed outside the terminal
 *  (composer, search, modals) never cross the terminal WS, so the keystroke check misses them. */
const GESTURES = ["pointerdown", "keydown", "wheel", "touchstart"] as const;

/** shouldBeacon is the pure predicate for "may this event send now", pinned by tests. */
export function shouldBeacon(
  trusted: boolean,
  visible: boolean,
  now: number,
  lastSent: number,
  interval = ATTENTION_INTERVAL_MS,
): boolean {
  if (!trusted) return false; // synthetic event (automated tests, extensions, our own dispatch)
  if (!visible) return false; // a background tab is not being looked at
  return now - lastSent >= interval;
}

/** wireAttentionBeacon installs the listeners; returns the unsubscribe.
 *
 * requireTrusted is a seam for tests. Events made by jsdom's dispatchEvent are always
 * isTrusted=false (own and non-configurable, so it cannot be faked), and opening this seam is
 * the only way to exercise the sending side. The default is true and production callers (the App
 * boot) pass no argument; that synthetic events are not counted is pinned by a separate test. */
export function wireAttentionBeacon({ requireTrusted = true } = {}): () => void {
  // Do not beacon right after boot (now, not 0): merely opening the screen must not claim
  // presence. Counting starts at the first interaction, so a tab left open never sends once.
  let lastSent = Date.now();
  const onGesture = (e: Event) => {
    const trusted = e.isTrusted || !requireTrusted;
    if (!shouldBeacon(trusted, document.visibilityState === "visible", Date.now(), lastSent)) return;
    lastSent = Date.now();
    // The response is ignored. A 409 racing a stop only means one presence record was dropped,
    // and the next interaction sends again; a toast here would surface an unrelated error on an
    // unrelated action.
    void workspaceAttention();
  };
  for (const type of GESTURES) {
    window.addEventListener(type, onGesture, { capture: true, passive: true });
  }
  return () => {
    for (const type of GESTURES) window.removeEventListener(type, onGesture, { capture: true });
  };
}
