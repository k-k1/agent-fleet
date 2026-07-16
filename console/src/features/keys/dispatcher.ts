// The keyboard dispatcher: one capture-phase window keydown listener that owns all
// app shortcuts. It runs BEFORE xterm's key handler (term.ts) and React's onKeyDown
// (both are DOM-tree descendants of window), so on a match it preventDefault +
// stopPropagation and the terminal / inputs never see the key. On no match it does
// nothing, so ordinary typing flows straight through. Wired once from the App boot
// effect via wireKeys(), which returns a cleanup (StrictMode-safe; mirrors
// layout/store.ts wireLayoutHistory).
//
// Escape is deliberately NOT claimed here except while a leader is pending — escLayer
// closes overlays at bubble phase and does not stopPropagation, so a capture-phase
// stop on Escape would break every modal/menu close. See the plan's "Escape landmine".
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { hasOpenOverlay } from "../../lib/escLayer.ts";
import { getSettings } from "../../lib/settings.ts";
import { eventChordString, shouldIgnore } from "../../lib/keys/chords.ts";
import { matchDirect, resolveLeader, isLeaderPrefix } from "../../lib/keys/registry.ts";
import type { KeyContext } from "../../lib/keys/registry.ts";
import { useKeysStore } from "./store.ts";
import { effectiveCommands, boundChord, APP_LEADER, APP_PALETTE, APP_CHEAT } from "./bindings.ts";

// The reserved app chords are read LIVE from the keybinding store (boundChord) on each
// keydown, so a rebind in Settings takes effect immediately without re-wiring. "" means
// the user unbound it (e.g. freed the leader for a pure terminal) — never matched.

const LEADER_TIMEOUT = 3000; // ms: a dangling leader auto-cancels
const WHICHKEY_DELAY = 350; // ms before the which-key overlay reveals

function focusedKind(): KeyContext["focusedKind"] {
  const el = document.activeElement as HTMLElement | null;
  if (!el) return "other";
  // The xterm helper textarea is a TEXTAREA, so check terminal FIRST.
  if (typeof el.closest === "function" && el.closest(".xterm")) return "terminal";
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable) return "input";
  return "other";
}

export function buildContext(): KeyContext {
  const ap = activePane(useLayoutStore.getState().layout);
  const ks = useKeysStore.getState();
  return {
    region: ks.activeRegion,
    focusedKind: focusedKind(),
    leaderPending: ks.leaderPending,
    activePaneKind: ap ? ap.content.kind : null,
  };
}

export function wireKeys(): () => void {
  let leaderTimer: number | null = null;
  let whichKeyTimer: number | null = null;

  const clearTimers = () => {
    if (leaderTimer != null) window.clearTimeout(leaderTimer);
    if (whichKeyTimer != null) window.clearTimeout(whichKeyTimer);
    leaderTimer = whichKeyTimer = null;
  };
  const cancelLeader = () => {
    clearTimers();
    useKeysStore.getState().setLeader(null);
  };
  const enterLeader = (path: string[]) => {
    clearTimers();
    useKeysStore.getState().setLeader(path);
    // Delay the overlay so a fast two-key sequence (e.g. leader p r) doesn't flash it.
    whichKeyTimer = window.setTimeout(() => useKeysStore.getState().setWhichKey(true), WHICHKEY_DELAY);
    leaderTimer = window.setTimeout(cancelLeader, LEADER_TIMEOUT);
  };
  const consume = (e: KeyboardEvent) => {
    e.preventDefault();
    e.stopPropagation(); // NOT stopImmediatePropagation — sibling window-capture listeners (PaneFind) stay live
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (shouldIgnore(e)) return; // IME composition / auto-repeat
    const chord = eventChordString(e);
    if (chord == null) return; // modifier-only keydown — keep waiting

    // Live-resolved reserved chords (respect user rebinds; "" = unbound → never matched,
    // since a real chord is always non-empty).
    const LEADER = boundChord(APP_LEADER);
    const PALETTE = boundChord(APP_PALETTE);
    const CHEAT = boundChord(APP_CHEAT);
    const commands = effectiveCommands();

    const ks = useKeysStore.getState();

    // --- Leader pending: the next key advances or resolves the sequence (swallowed) ---
    if (ks.leaderPending) {
      if (chord === "escape" || chord === LEADER) {
        consume(e);
        cancelLeader();
        return;
      }
      const nextPath = [...ks.leaderPath, chord];
      const ctx = buildContext();
      const cmd = resolveLeader(commands, nextPath, ctx);
      if (cmd) {
        consume(e);
        cancelLeader();
        cmd.run(ctx);
        return;
      }
      if (isLeaderPrefix(commands, nextPath, ctx)) {
        consume(e);
        enterLeader(nextPath);
        return;
      }
      // Dead end: swallow (don't leak the key to the terminal) and cancel.
      consume(e);
      cancelLeader();
      return;
    }

    // --- Not in leader mode. A modal/menu/palette owning the keyboard wins. ---
    if (hasOpenOverlay()) return;

    const ctx = buildContext();

    // Terminal-input priority (Settings): while a terminal is focused, yield every app
    // chord to xterm EXCEPT the leader — the single guaranteed gateway to which-key /
    // palette. An in-progress leader sequence is handled above, so it always completes.
    // With the leader itself unbound, nothing escapes → a fully pure terminal, by choice.
    if (getSettings().terminalPriority && ctx.focusedKind === "terminal" && chord !== LEADER) {
      return;
    }

    if (chord === LEADER) {
      consume(e);
      enterLeader([]);
      return;
    }
    if (chord === PALETTE) {
      consume(e);
      useKeysStore.getState().openPalette();
      return;
    }
    // "?" opens the cheat-sheet, but only when not typing — in a terminal/input it's a
    // literal question mark. (The leader → ? path stays available regardless.)
    if (chord === CHEAT && ctx.focusedKind !== "input" && ctx.focusedKind !== "terminal") {
      consume(e);
      useKeysStore.getState().openCheat();
      return;
    }
    const cmd = matchDirect(commands, chord, ctx);
    if (cmd) {
      consume(e);
      cmd.run(ctx);
      return;
    }
    // No match → let the key flow to the terminal / inputs untouched.
  };

  window.addEventListener("keydown", onKeyDown, true); // capture: beat xterm + React
  return () => {
    clearTimers();
    window.removeEventListener("keydown", onKeyDown, true);
  };
}
