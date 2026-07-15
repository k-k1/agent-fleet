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
import { eventChordString, shouldIgnore, canonical } from "../../lib/keys/chords.ts";
import { matchDirect, resolveLeader, isLeaderPrefix } from "../../lib/keys/registry.ts";
import type { KeyContext } from "../../lib/keys/registry.ts";
import { useKeysStore } from "./store.ts";
import { ALL_COMMANDS } from "./commands.ts";

// Reserved app chords (rebindable in P5). Canonicalized so comparisons are exact.
const LEADER = canonical("mod+k");
const PALETTE = canonical("mod+p");

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
      const cmd = resolveLeader(ALL_COMMANDS, nextPath, ctx);
      if (cmd) {
        consume(e);
        cancelLeader();
        cmd.run(ctx);
        return;
      }
      if (isLeaderPrefix(ALL_COMMANDS, nextPath, ctx)) {
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

    const ctx = buildContext();
    const cmd = matchDirect(ALL_COMMANDS, chord, ctx);
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
