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
import { eventChordString, eventKeyChordString } from "../../lib/keys/chords.ts";
import { matchDirect, resolveLeader, isLeaderPrefix } from "../../lib/keys/registry.ts";
import type { KeyContext } from "../../lib/keys/registry.ts";
import { useKeysStore } from "./store.ts";
import { effectiveCommands, boundChord, APP_LEADER, APP_PALETTE, APP_CHEAT } from "./bindings.ts";
import { isScrollKey, findScroller, paneScrollDelta, SCROLLABLE_KINDS, VIEWER_KINDS } from "../../lib/keyScroll.ts";

// The reserved app chords are read LIVE from the keybinding store (boundChord) on each
// keydown, so a rebind in Settings takes effect immediately without re-wiring. "" means
// the user unbound it (e.g. freed the leader for a pure terminal) — never matched.

const LEADER_TIMEOUT = 3000; // ms: a dangling leader (overlay not yet shown) auto-cancels
// Once the which-key overlay is actually visible the user is reading it, so a stray-press
// guard no longer applies — give them a generous window to find the key before auto-cancel,
// while still recovering if they walk away (never a permanently stuck overlay).
const LEADER_TIMEOUT_OPEN = 15000;
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

// True when focus is inside a shell/ssm terminal specifically (not an agent chat terminal).
// TerminalView tags those panes' container with `.terminal-shellssm`. Used by the
// shellTermPassthrough gate to make plain shells pure terminals without affecting agents.
function isShellSsmTerminal(): boolean {
  const el = document.activeElement as HTMLElement | null;
  return !!(el && typeof el.closest === "function" && el.closest(".terminal-shellssm"));
}

// The currently-focused element an IME could compose into (a text field or the terminal's
// helper textarea), or null if focus is somewhere inert. Blurring it on leader entry is how
// we "turn IME off" for the sequence — see dropIME() below. SELECT is excluded (no IME).
function imeTarget(): HTMLElement | null {
  const el = document.activeElement as HTMLElement | null;
  if (!el || el === document.body) return null;
  if (typeof el.closest === "function" && el.closest(".xterm")) return el; // terminal textarea
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable) return el;
  return null;
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
  // The editable element we blurred to drop IME when the leader was pressed, so we can hand
  // focus (and its IME) back if the sequence is cancelled or ends on a command that doesn't
  // move focus itself.
  let savedFocus: HTMLElement | null = null;

  // "Turn IME off" for the leader sequence. An active IME (Japanese kana-kanji conversion,
  // say) would compose
  // the follow-up keys (p, r, …) into the focused field/terminal, and shouldIgnore() would
  // then swallow them — the sequence breaks and stray characters can leak in. Web pages can't
  // toggle the OS IME, but blurring the compose target ends any composition and parks focus on
  // <body>, where keys arrive as plain keydowns the dispatcher reads cleanly.
  const dropIME = () => {
    const el = imeTarget();
    if (el) {
      savedFocus = el;
      el.blur();
    }
  };
  // Return focus (and thus IME) to whatever we blurred. A focus-moving command re-focuses on
  // its own (often via rAF, which lands after this), so calling this on a completed command is
  // safe — the command's own focus wins; a no-op command keeps its terminal focused.
  const restoreFocus = () => {
    if (savedFocus && document.contains(savedFocus)) savedFocus.focus();
    savedFocus = null;
  };

  const clearTimers = () => {
    if (leaderTimer != null) window.clearTimeout(leaderTimer);
    if (whichKeyTimer != null) window.clearTimeout(whichKeyTimer);
    leaderTimer = whichKeyTimer = null;
  };
  const cancelLeader = () => {
    clearTimers();
    useKeysStore.getState().setLeader(null);
    restoreFocus();
  };
  // `immediate` reveals the overlay without the WHICHKEY_DELAY — used when stepping BACK
  // (Backspace) while the overlay is already open, so the shorter path repaints in place
  // with no flash-off/flash-on. Fresh entries (leader just pressed) keep the delay so a
  // fast two-key sequence never flashes the hint.
  const enterLeader = (path: string[], immediate = false) => {
    clearTimers();
    useKeysStore.getState().setLeader(path);
    const reveal = () => {
      useKeysStore.getState().setWhichKey(true);
      // The overlay is now on screen and the user is scanning it — swap the short stray-press
      // guard for the longer reading window so the hint doesn't vanish mid-search. Clear the
      // pending short timer first: overwriting the handle would leave it live and it'd still
      // cancel the leader at the 3s mark, silently shrinking the 15s window.
      if (leaderTimer != null) window.clearTimeout(leaderTimer);
      leaderTimer = window.setTimeout(cancelLeader, LEADER_TIMEOUT_OPEN);
    };
    if (immediate) {
      reveal();
      return;
    }
    // Delay the overlay so a fast two-key sequence (e.g. leader p r) doesn't flash it.
    whichKeyTimer = window.setTimeout(reveal, WHICHKEY_DELAY);
    leaderTimer = window.setTimeout(cancelLeader, LEADER_TIMEOUT);
  };
  const consume = (e: KeyboardEvent) => {
    e.preventDefault();
    e.stopPropagation(); // NOT stopImmediatePropagation — sibling window-capture listeners (PaneFind) stay live
  };

  // Keyboard-scroll the ACTIVE read-only viewer pane (file / diff / scm / …). Keyed off the
  // active pane rather than DOM focus, so it works whether or not the pane's body is focused;
  // its own scroller is located by geometry (findScroller), so every viewer kind works without
  // per-view wiring. Bails out when typing (input/terminal), during a leader sequence, or with
  // an overlay open — those keep the arrows. Returns true once it has handled + consumed.
  const maybeScrollActivePane = (e: KeyboardEvent): boolean => {
    if (!isScrollKey(e) || e.isComposing || hasOpenOverlay()) return false;
    if (useKeysStore.getState().leaderPending) return false;
    if (focusedKind() !== "other") return false; // an input/terminal owns its arrows
    const ap = activePane(useLayoutStore.getState().layout);
    if (!ap || !SCROLLABLE_KINDS.has(ap.content.kind)) return false;
    const paneEl = document.querySelector<HTMLElement>(`.pane[data-pane-id="${CSS.escape(ap.id)}"]`);
    const el = findScroller(paneEl);
    if (!el) return false;
    // Plain nav keys (↑/↓, PageUp/Down, Home/End, Space) only on a PURE viewer whose scroller
    // itself holds focus — so a focused button/link inside the view, or an interactive scm /
    // changes pane, keeps its keys. Modified gestures (Shift/Ctrl+↑↓, Ctrl+[ ]) drive any
    // scrollable pane regardless of focus.
    const allowPlain = VIEWER_KINDS.has(ap.content.kind) && document.activeElement === el;
    const delta = paneScrollDelta(e, el, allowPlain);
    if (delta === null) return false;
    el.scrollTop = Math.max(0, Math.min(el.scrollHeight - el.clientHeight, el.scrollTop + delta));
    consume(e);
    return true;
  };

  const onKeyDown = (e: KeyboardEvent) => {
    // Scroll the active viewer pane. Handled before the auto-repeat guard so holding the key
    // keeps scrolling (unlike one-shot shortcuts, which must not fire on auto-repeat).
    if (maybeScrollActivePane(e)) return;
    if (e.repeat) return; // never fire a shortcut on auto-repeat (a held-down key)
    const chord = eventChordString(e); // from e.code — valid even while an IME composes
    if (chord == null) return; // modifier-only keydown — keep waiting
    // An IME is mid-composition (Japanese conversion reports isComposing / keyCode 229).
    const composing = e.isComposing === true || e.keyCode === 229;

    // Live-resolved reserved chords (respect user rebinds; "" = unbound → never matched,
    // since a real chord is always non-empty).
    const LEADER = boundChord(APP_LEADER);
    const PALETTE = boundChord(APP_PALETTE);
    const CHEAT = boundChord(APP_CHEAT);
    const commands = effectiveCommands();

    const ks = useKeysStore.getState();

    // --- Leader pending: the next key advances or resolves the sequence (swallowed). We
    // blurred the compose target on entry, so these keys arrive un-composed; process them
    // regardless of `composing` so the sequence can never be stranded by a stray IME state. ---
    if (ks.leaderPending) {
      if (chord === "escape" || chord === LEADER) {
        consume(e);
        cancelLeader();
        return;
      }
      // Backspace steps ONE level back up the sequence (which-key convention), vs. Escape's
      // full cancel. From a submenu (e.g. "p") it returns to the ROOT menu (path []), which is
      // itself a valid open state showing the top-level groups — so it stays open. Only a
      // Backspace at that already-empty root has nowhere to go and backs out entirely. Re-enter
      // with an immediate reveal when the overlay is already up so the breadcrumb repaints
      // without a flash-off/flash-on.
      if (chord === "backspace") {
        consume(e);
        if (ks.leaderPath.length === 0) cancelLeader();
        else enterLeader(ks.leaderPath.slice(0, -1), ks.whichKeyOpen);
        return;
      }
      const ctx = buildContext();
      // Same layout tolerance as the direct-accelerator path below: a sequence key can be
      // punctuation whose physical position differs by layout (JIS ] → .code "Backslash",
      // so "p ]" would decode as "p \\" = wrap). Try the .key-derived chord first, then
      // .code, and take whichever resolves to a command or a valid prefix. For non-
      // punctuation keys eventKeyChordString returns null → candidates is just [chord].
      const kchord = eventKeyChordString(e);
      const candidates = kchord && kchord !== chord ? [kchord, chord] : [chord];
      for (const c of candidates) {
        const nextPath = [...ks.leaderPath, c];
        const cmd = resolveLeader(commands, nextPath, ctx);
        if (cmd) {
          consume(e);
          cancelLeader(); // hands focus back; a focus-moving command overrides it next
          cmd.run(ctx);
          return;
        }
        if (isLeaderPrefix(commands, nextPath, ctx)) {
          consume(e);
          enterLeader(nextPath);
          return;
        }
      }
      // Dead end: swallow (don't leak the key to the terminal) and cancel.
      consume(e);
      cancelLeader();
      return;
    }

    // --- Not in leader mode. While an IME is composing, defer to it — EXCEPT the leader
    // chord, which drops IME and opens command mode even mid-composition (Ctrl+K is never part of a
    // composition). Every other key flows to the IME untouched. ---
    if (composing) {
      if (chord === LEADER && !hasOpenOverlay()) {
        consume(e);
        dropIME();
        enterLeader([]);
      }
      return;
    }

    // A modal/menu/palette owning the keyboard wins.
    if (hasOpenOverlay()) return;

    const ctx = buildContext();

    // Terminal-input priority (Settings): while a terminal is focused, yield app chords
    // to xterm so they reach the shell/PTY.
    if (ctx.focusedKind === "terminal") {
      const s = getSettings();
      // shellTermPassthrough: for a FOCUSED shell/ssm terminal only, yield EVERY app chord —
      // including the leader (Ctrl/⌘+K) and palette (Ctrl/⌘+P) — so Ctrl+K (kill-line),
      // Ctrl+P (history-prev) etc. reach the shell. A fully pure terminal; return to app
      // shortcuts by focusing another pane. (An in-progress leader sequence is handled above.)
      if (s.shellTermPassthrough && isShellSsmTerminal()) return;
      // terminalPriority (all terminals): yield every chord EXCEPT the leader — the single
      // guaranteed gateway to which-key / palette. With the leader unbound, nothing escapes.
      if (s.terminalPriority && chord !== LEADER) return;
    }

    if (chord === LEADER) {
      consume(e);
      dropIME(); // turn IME off for the sequence the moment the leader is pressed
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
    // Direct accelerators. Punctuation keys (only Alt+[ / Alt+] today) are matched by
    // .key FIRST, then .code; every other key uses .code only (eventKeyChordString returns
    // null for letters/digits/named keys). A key's physical POSITION varies by layout, and
    // our .code map uses US-keycap naming: on a JIS keyboard the [ key reports .code
    // "BracketRight" and the ] key reports .code "Backslash", so by .code the [ key would
    // wrongly fire pane.next and the ] key nothing. .key ("[" / "]") is what the user
    // actually pressed, so trying it first makes the LABELED key win; .code stays the
    // fallback for OSes/layouts where a modifier mutates .key (e.g. macOS ⌥ → "‘").
    const kchord = eventKeyChordString(e);
    const candidates = kchord && kchord !== chord ? [kchord, chord] : [chord];
    for (const c of candidates) {
      const cmd = matchDirect(commands, c, ctx);
      if (cmd) {
        consume(e);
        cmd.run(ctx);
        return;
      }
    }
    // No match → let the key flow to the terminal / inputs untouched.
  };

  window.addEventListener("keydown", onKeyDown, true); // capture: beat xterm + React
  return () => {
    clearTimers();
    window.removeEventListener("keydown", onKeyDown, true);
  };
}
