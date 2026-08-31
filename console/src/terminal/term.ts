// xterm.js terminal manager. Terminals live outside React so each instance and its
// WebSocket survive view switches (a pane's DOM container stays mounted and we just
// open() the term into it once). Originally a module singleton; now keyed by paneId
// so the console can show several sessions side by side (split panes). Originally
// ported from the Phase 1 Console.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { coarsePointer } from "../lib/device.ts";
import { WebglAddon } from "@xterm/addon-webgl";
import { CanvasAddon } from "@xterm/addon-canvas";
import "@xterm/xterm/css/xterm.css";
import { wsURL, rel } from "../core/api/client.ts";
import { isAuthExpired } from "../core/auth/authExpired.ts";
import { getSettings, subscribe as subscribeSettings, termFontStack } from "../lib/settings.ts";
import { askConfirm } from "../ui/confirmBridge.ts";
import { t as tr } from "../lib/i18n/index.ts";
import { zoom } from "../app/viewport.ts";

// One entry per pane. { term, fitAddon, ws, session, sessionListeners, ro }.
// A placeholder (from an early onSession) may hold only session + sessionListeners,
// so the terminal-side fields are optional.
interface Inst {
  term?: Terminal | null;
  fitAddon?: FitAddon;
  ws?: WebSocket | null;
  session: string | null;
  sessionListeners: Set<(name: string | null) => void>;
  ro?: ResizeObserver | null;
  dropped?: boolean;
  hb?: ReturnType<typeof setInterval>; // heartbeat timer (see startHeartbeat)
  rttTimer?: ReturnType<typeof setInterval>; // RTT sampling timer (see startHeartbeat)
  lastPong?: number; // ms of the last pong seen on the current socket
  pingAt?: number; // performance.now() of the ping still awaiting a pong (see noteRtt)
  rttLog?: number[]; // recent round-trip samples, newest last (RTT_LOG_MAX)
  rttAt?: number; // Date.now() of the newest sample
  rttListeners?: Set<(s: RttStats | null) => void>;
  pongWaiters?: ((rtt: number) => void)[]; // probeTerminalRtt's sequential sampler
  rx?: boolean; // any PTY byte received on the CURRENT socket (see ensureAttached)
  connAt?: number; // ms when the CURRENT socket began connecting (stall watchdog — see ensureAttached)
  connWd?: ReturnType<typeof setTimeout>; // connect-stall watchdog timer (see attach)
  webgl?: WebglAddon | null; // live WebGL renderer addon (dropped while hidden — see hideTerm)
  canvas?: CanvasAddon | null; // 2D fallback renderer, when WebGL is unavailable/lost/over budget (see loadCanvas)
}
const insts = new Map<string, Inst>();
function inst(paneId: string): Inst | null {
  return insts.get(paneId) || null;
}

// listeners notified when a pane's attached session name changes (so the UI can
// show it). Registered per pane; survive across ensureTerm calls.
export function onSession(paneId: string, fn: (name: string | null) => void) {
  let it = insts.get(paneId);
  if (!it) {
    // Allow subscribing before ensureTerm: stash a listener set on a placeholder.
    it = { sessionListeners: new Set(), session: null };
    insts.set(paneId, it);
  }
  it.sessionListeners.add(fn);
  fn(it.session ?? null);
  return () => it.sessionListeners && it.sessionListeners.delete(fn);
}
function setSession(it: Inst, name: string | null) {
  it.session = name;
  for (const fn of it.sessionListeners) fn(name);
}

// Clipboard helpers. Browsers route Ctrl+C/Ctrl+V inside a focused terminal to the
// PTY (SIGINT / literal ^V), NOT the system clipboard — so plain copy/paste never
// worked. We wire explicit gestures (copy-on-select, Ctrl+Shift+C / Ctrl+Insert,
// right/middle-click paste, Shift+Insert / Ctrl+Shift+V) to the async Clipboard API,
// leaving Ctrl+C free to interrupt the foreground program.
function copySelection(term: Terminal) {
  const sel = term && term.getSelection();
  if (sel && navigator.clipboard) navigator.clipboard.writeText(sel).catch(() => {});
}
// Warn before pasting risky content into the terminal. At a raw shell prompt a paste
// that contains newlines runs line-by-line (each Enter executes), and a very large paste
// can flood the pane — both are easy to trigger by an accidental right/middle-click. When
// the clipboard has a newline or exceeds PASTE_WARN_CHARS we confirm first; otherwise it
// pastes straight through. Covers every paste gesture (they all funnel through here).
const PASTE_WARN_CHARS = 1000;
function pasteClipboard(term: Terminal) {
  if (!term || !navigator.clipboard) return;
  navigator.clipboard
    .readText()
    .then((t) => {
      if (!t) return;
      // term.paste() routes through onData (incl. bracketed-paste wrapping) → PTY.
      // In bracketed-paste mode (claude/codex, vim, readline shells, …) the paste is
      // delivered wrapped and is NOT auto-executed, so the newline/size risks don't
      // apply — paste straight through and skip the nag. The warning is only for a raw
      // prompt (bracketed paste off) where each pasted newline runs immediately.
      if (term.modes.bracketedPasteMode) {
        term.paste(t);
        return;
      }
      const newline = /[\r\n]/.test(t);
      if (!newline && t.length <= PASTE_WARN_CHARS) {
        term.paste(t);
        return;
      }
      const lines = t.split(/\r\n|[\r\n]/).length;
      askConfirm({
        title: tr("onb.paste_confirm_title"),
        body:
          tr("onb.paste_chars", { count: t.length }) +
          (newline ? tr("onb.paste_lines", { lines }) : "") +
          tr("onb.paste_suffix") +
          (newline ? tr("onb.paste_newline_warn") : ""),
        confirmLabel: tr("onb.paste_confirm"),
        danger: true,
      }).then((ok) => {
        if (ok) term.paste(t);
      });
    })
    .catch(() => {});
}

// Apply the current font family/size from settings to every live terminal, live,
// when the user changes them. Registered once for all panes.
let settingsSubscribed = false;
function applyTermSettingsAll() {
  const s = getSettings();
  for (const it of insts.values()) {
    if (!it.term) continue;
    it.term.options.fontFamily = termFontStack(s.termFont);
    it.term.options.fontSize = s.termSize;
    fitInst(it);
    try {
      it.term.refresh(0, it.term.rows - 1);
    } catch {}
  }
}

// Refit every live terminal — the window/visualViewport resize path. Per-pane split
// resizes don't fire window resize, so each pane also gets its own ResizeObserver.
function fitAll() {
  for (const it of insts.values()) fitInst(it);
}
let globalResizeWired = false;
function wireGlobalResize() {
  if (globalResizeWired) return;
  globalResizeWired = true;
  window.addEventListener("resize", fitAll);
  // On mobile the soft keyboard shrinks the layout viewport rather than firing a
  // window resize; visualViewport fires its own resize so grids refit and the
  // prompt isn't left hidden behind the keyboard.
  if (window.visualViewport)
    window.visualViewport.addEventListener("resize", () => {
      fitAll();
      // Keyboard opened/closed (or rotated): re-place the focused prompt above it,
      // or clear the shift when it's gone.
      keepInputVisible(focusedInst());
    });
}

// Returning to the app reconnects any dropped panes. This is the recovery path for
// a single pane: with no other pane to switch to, the per-terminal focus event
// never fires, so a drop while idle/backgrounded would otherwise sit at
// "[disconnected]" with no way to re-trigger. reconnect() is a no-op on panes that
// aren't dropped, so firing it for all is safe.
let globalReconnectWired = false;
function wireGlobalReconnect() {
  if (globalReconnectWired) return;
  globalReconnectWired = true;
  const recoverAll = (redraw = false) => {
    for (const id of insts.keys()) reconnect(id);
    // Mobile browsers may reclaim a background tab's WebGL context without
    // dispatching webglcontextlost. The PTY then remains connected while its
    // canvas stays black, so socket reconnection alone cannot recover it.
    if (redraw) {
      for (const it of insts.values()) redrawVisible(it);
    }
  };
  window.addEventListener("focus", () => recoverAll());
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") recoverAll(true);
  });
}

function fitInst(it: Inst | null | undefined) {
  try {
    if (!it || !it.fitAddon || !it.term) return;
    // FitAddon.fit() bails (proposeDimensions returns nothing) whenever the cached
    // character cell is 0×0 — the state a terminal is left in when it's open()ed into
    // a pane that has no laid-out size yet. xterm only re-measures the cell lazily
    // (some render/resize paths, gated on hasValidSize), and a paused/off-screen pane
    // can miss all of them, so the grid stays pinned at the 80×24 default and never
    // recovers on its own. When the cached size is invalid, force a re-measure first so
    // this fit can actually size the grid. Cheap: it only runs in the broken state
    // (a healthy pane has hasValidSize === true and this is skipped).
    /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
    const cs = (it.term as any)._core?._charSizeService;
    if (cs && !cs.hasValidSize && typeof cs.measure === "function") cs.measure();
    it.fitAddon.fit();
  } catch {}
}

// forceFit re-syncs a visible pane's grid AND forces a full renderer repaint. Use it on
// reveal / re-open / re-attach / mount-settle, where the pane can be the right grid shape
// yet never have painted (a split-pane mount race, or a canvas whose atlas/context went
// stale while hidden). fitInst() re-measures + refits, which repaints on its own WHEN the
// settled size yields a different grid — but fit() is a NO-OP when the proposed grid equals
// the current one, so an unchanged-but-unpainted pane stays black. That is why "moving the
// pane fixes it": the size change forces a full renderer clear+repaint. Do that clear+
// repaint unconditionally here (via the same _renderService.clear() that fit() uses on a
// resize, plus clearTextureAtlas() for the canvas renderer's glyph cache), so a reveal
// never leaves a black pane waiting on a size change that may never come. All the reach-ins
// are private xterm internals (like FitAddon's own _core reach-in) and guarded, so a
// version bump just makes them no-op rather than throw.
// webglLost reports a WebGL renderer whose context died WITHOUT firing our
// onContextLoss handler — the desktop variant of the silent mobile reclaim.
// Browsers cap live WebGL contexts (~16 per tab) and evict the oldest when the
// cap is hit; mirror⇄terminal toggling rebuilds contexts (hideTerm/revealTerm),
// so heavy switching can silently kill the context of a pane that stayed
// visible the whole time. Nothing can paint into a dead context — refresh(),
// clear(), the whole recovery arsenal draws into the void — so it must be
// DETECTED and the renderer rebuilt. Private reach-in (like FitAddon's own
// _core usage), guarded so an xterm bump degrades to "never lost".
function webglLost(it: Inst): boolean {
  try {
    /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
    const gl = (it.webgl as any)?._renderer?._gl;
    return !!gl?.isContextLost?.();
  } catch {
    return false;
  }
}

function forceFit(it: Inst | null | undefined) {
  if (!it || !it.term) return;
  const el = it.term.element;
  if (!el || !el.isConnected || el.getClientRects().length === 0) return;
  // A silently-dead WebGL context makes every repaint below a no-op: rebuild the
  // renderer first so clear/refresh have a live target. Runs only when the
  // context is actually lost (a frozen canvas), so no swap-flicker on the hot
  // recovery paths (focus/active/watchdog) in the healthy case.
  if (it.webgl && webglLost(it)) {
    dropWebgl(it);
    loadWebgl(it);
  }
  fitInst(it);
  try {
    /* eslint-disable @typescript-eslint/no-explicit-any */
    // Canvas-backed renderers ONLY (WebGL or the 2D canvas fallback): a stale/blank canvas
    // or a corrupted glyph atlas must be cleared + the atlas rebuilt before the repaint
    // below can show correct pixels. Both are gated off on touch devices (loadWebgl /
    // loadCanvas), so this branch is desktop-only and never hits the DOM renderer.
    if (it.webgl || it.canvas) {
      const core = (it.term as any)._core;
      core?._renderService?.clear?.();
      (it.term as any).clearTextureAtlas?.();
    }
    /* eslint-enable @typescript-eslint/no-explicit-any */
    // Repaint every row from the buffer. On the DOM renderer (touch/mobile, or wherever WebGL
    // is unavailable) this refresh IS the whole recovery — we deliberately do NOT call
    // _renderService.clear() there. clear() blanks the row elements synchronously and defers
    // the repaint to the next animation frame; a phone's frequent hide/reveal/soft-keyboard/
    // visibility churn routinely drops that frame, and xterm does not auto-repaint on reveal,
    // so the rows stay permanently EMPTY — the "mobile TUI goes black" regression that every
    // forceFit call site (reveal / focus / active / redraw) could trigger. refresh() alone
    // repaints the dirty rows without ever blanking first (DOM rows aren't a stale canvas), so
    // it is strictly safer. Verified in a coarse-pointer headless harness: clear()→hide→show
    // leaves it black; refresh-only recovers it and never flashes blank.
    it.term.refresh(0, it.term.rows - 1);
  } catch {}
}

// Show the mobile soft keyboard (GBoard) only while a PTY is actually connected.
// inputmode="none" lets the terminal still take focus — so tapping a dropped pane
// still triggers reconnect — without the keyboard filling a phone screen over a
// disconnected/empty terminal. Desktop browsers ignore inputmode, so typing there
// is unaffected.
function setSoftKeyboard(it: Inst | null | undefined, enabled: boolean) {
  const ta = it && it.term && it.term.textarea;
  if (ta) ta.inputMode = enabled ? "" : "none";
}

// The pane whose terminal currently holds focus (its hidden helper textarea is the
// active element), or null.
function focusedInst() {
  const ae = document.activeElement;
  for (const it of insts.values()) {
    if (it.term && it.term.textarea === ae) return it;
  }
  return null;
}

// Keep the focused terminal's prompt above the mobile soft keyboard. GBoard overlays
// the bottom of the page without shrinking the layout (viewport default
// resizes-visual), and the app is locked to 100% with no scrollable overflow — so
// there's nothing to scroll. Instead we translate the main area up by however much
// the focused pane sits behind the keyboard. A bottom/single pane computes a
// positive overlap and rises; a top pane (its bottom already above the keyboard)
// computes a non-positive overlap and stays put. Desktop / no-keyboard → overlap
// path is skipped and any prior shift is cleared.
function keepInputVisible(it: Inst | null) {
  const vv = window.visualViewport;
  const main = document.querySelector<HTMLElement>(".main");
  if (!vv || !main) return;
  main.style.transform = ""; // measure against the natural (untranslated) position
  const el = it && it.term && it.term.element;
  if (!el) return;
  // vv.height alone shrinks on a pinch zoom too (at 2x it covers half the layout), so
  // scale it back up by the zoom before comparing: a plain zoom must not read as a keyboard
  // and shift .main out from under the user's fingers. The overlap below stays in raw
  // vv units — offsetTop + height is the visible band in layout coordinates either way.
  const keyboard = window.innerHeight - vv.height * zoom(vv); // ~0 unless a keyboard is up
  if (keyboard < 150) return; // ignore URL-bar show/hide; only react to a keyboard
  const pane = el.closest(".pane") || el;
  const overlap = pane.getBoundingClientRect().bottom - (vv.offsetTop + vv.height);
  if (overlap > 0) main.style.transform = `translateY(-${Math.ceil(overlap) + 4}px)`;
}

// evictForeignTerms enforces the one-container-one-terminal half of the paneId ==
// terminal identity contract (service.ts): whoever takes `el` gets it to itself.
//
// The tabbed layout REUSES one React component — and therefore one container div —
// for every tab of a cell: selecting a tab only changes the paneId prop, it does not
// remount TerminalView. So ensureTerm is called for the newly selected pane on the
// very div that still holds the previous tab's xterm, and term.open() APPENDS. The
// container ended up with both elements: the older one stays first and fills the
// pane, the newly selected one is pushed below it and out of view. The pane then
// shows the PREVIOUS session's live screen — its TUI and its tmux status line — while
// the header, the PTY socket and every keystroke belong to the session the user
// actually selected. That is the "ターミナルを開くと別のセッションの tmux にアタッチ
// される" report: nothing mis-attached, the pane was painting the wrong terminal.
//
// Detaching is safe and reversible: the evicted pane keeps its instance, socket and
// scrollback, and its own ensureTerm re-parents the element (and repaints it via
// forceFit) when that tab comes back — the same path a React remount already takes.
function evictForeignTerms(it: Inst, el: HTMLElement) {
  if (!el) return;
  for (const other of insts.values()) {
    if (other === it || !other.term) continue;
    const oel = other.term.element;
    if (!oel || oel.parentElement !== el) continue;
    oel.remove();
    // An evicted terminal is off-screen by definition, so treat it exactly like
    // hideTerm: give the WebGL context back. A detached canvas holding a live
    // context is the state this file warns about (a silent reclaim leaves a
    // renderer that looks alive and can never paint again), and with up to
    // MAX_TABS=24 terminals in a layout the ~16-contexts-per-tab browser cap is
    // reachable on its own. TerminalView's reveal effect re-runs on the paneId
    // change, so coming back rebuilds the renderer (revealTerm → loadWebgl);
    // should it not, xterm falls back to its DOM renderer — never black, but slow
    // enough with CJK to freeze the tab (loadCanvas), so the reveal path matters.
    dropWebgl(other);
    dropCanvas(other); // an off-screen pane holds no renderer resources at all
    // The ResizeObserver watches the CONTAINER, not the terminal, so leaving it
    // connected would keep refitting a pane that no longer lives there. ensureTerm
    // re-observes on the way back in.
    if (other.ro) {
      try {
        other.ro.disconnect();
      } catch {}
      other.ro = null;
    }
  }
}

// ensureTerm builds a pane's terminal once and opens it into `el`. Subsequent calls
// for the same pane re-open into the element if React remounted the container; the
// instance (and scrollback) persists.
export function ensureTerm(paneId: string, el: HTMLElement) {
  let it = insts.get(paneId);
  if (it && it.term) {
    evictForeignTerms(it, el);
    // Re-attach to the element if React remounted the container. xterm's open()
    // is a silent NO-OP once a terminal has opened (5.x) — calling it again
    // neither moves nor recreates the DOM, so the remounted container stayed
    // EMPTY forever: the pane renders pure black (no cursor, no background)
    // while the live xterm — still holding its PTY socket — sits parented to
    // the discarded old div (and the server keeps a zombie tmux attach for it).
    // Move the element by hand instead. Reparenting a canvas keeps its GL
    // context but may discard the drawn frame, and a kept instance may have sat
    // hidden for a while — so repaint every row afterwards (forceFit below,
    // same contract as revealTerm).
    if (el && it.term.element && it.term.element.parentElement !== el) el.appendChild(it.term.element);
    else if (el && !it.term.element) it.term.open(el);
    observe(it, el);
    forceFit(it);
    return it.term;
  }
  // May already hold a placeholder from an early onSession() subscription.
  if (!it) {
    it = { sessionListeners: new Set(), session: null };
    insts.set(paneId, it);
  }
  evictForeignTerms(it, el); // term.open() appends — the container must be ours alone
  const s0 = getSettings();
  const term = new Terminal({
    fontSize: s0.termSize,
    fontFamily: termFontStack(s0.termFont),
    theme: { background: "#1e1e1e" },
    cursorBlink: true,
    allowProposedApi: true,
  });
  it.term = term;
  it.ws = null;
  // Apply terminal font/size from settings, live, when the user changes them.
  if (!settingsSubscribed) {
    settingsSubscribed = true;
    subscribeSettings(applyTermSettingsAll);
  }
  wireGlobalResize();
  wireGlobalReconnect();
  const fitAddon = new FitAddon();
  it.fitAddon = fitAddon;
  term.loadAddon(fitAddon);
  // Unicode 11 widths so emoji / wide glyphs occupy 2 cells (no half-clipping).
  try {
    term.loadAddon(new Unicode11Addon());
    term.unicode.activeVersion = "11";
  } catch {}
  // Make URLs clickable (open in a new tab) — the /login auth URL wraps across
  // rows and breaks on copy, so clicking is the reliable path.
  try {
    term.loadAddon(new WebLinksAddon((e, uri) => window.open(uri, "_blank", "noopener")));
  } catch {}
  term.open(el);
  // OSC 52: with `set-clipboard on`, tmux emits the just-copied selection as an
  // OSC 52 sequence to its outer terminal (us). xterm has no built-in OSC 52 handler,
  // so a plain mouse drag-select in the terminal (which tmux, in mouse mode, turns into
  // a copy-mode selection) would never reach the browser clipboard. Register one that
  // decodes the base64 payload and writes it out — this is the copy half of the mouse
  // clipboard for shell/ssm panes. (Shift+drag still does a browser-native selection
  // handled by the mouseup path below; the two coexist.)
  try {
    term.parser.registerOscHandler(52, (data) => {
      // data is "<targets>;<base64>" e.g. "c;SGVsbG8=". "?" is a read request — ignore.
      const semi = data.indexOf(";");
      const b64 = semi >= 0 ? data.slice(semi + 1) : data;
      if (!b64 || b64 === "?" || !navigator.clipboard) return true;
      try {
        const bin = atob(b64);
        const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
        const text = new TextDecoder().decode(bytes);
        if (text) navigator.clipboard.writeText(text).catch(() => {});
      } catch {}
      return true; // handled — don't fall through to the OSC fallback
    });
  } catch {}
  // Mouse clipboard (terminal idiom): left-release auto-copies the current
  // selection; right/middle click pastes. This is the reliable path even where the
  // browser reserves Ctrl+Shift+C (DevTools) outside fullscreen.
  if (term.element) {
    term.element.addEventListener("mouseup", (ev) => {
      if (ev.button === 0 && term.hasSelection()) copySelection(term);
    });
    term.element.addEventListener("contextmenu", (ev) => {
      ev.preventDefault();
      pasteClipboard(term);
    });
    term.element.addEventListener("auxclick", (ev) => {
      if (ev.button === 1) {
        ev.preventDefault();
        pasteClipboard(term);
      }
    });
    // Touch scrolling: a phone has no wheel, so translate a one-finger vertical
    // drag into scrollback movement. Drag down to reveal older lines, up for newer
    // — the natural "content follows the finger" direction. We accumulate pixels and
    // scroll whole rows so it tracks smoothly. (In an alt-screen TUI there's no
    // scrollback, so scrollLines is a no-op there and the app's own keys page.)
    let lastY: number | null = null,
      acc = 0;
    const cellH = () => {
      const vp = term.element!.querySelector(".xterm-viewport");
      return vp && term.rows ? vp.clientHeight / term.rows : 18;
    };
    term.element.addEventListener(
      "touchstart",
      (ev) => {
        if (ev.touches.length === 1) {
          lastY = ev.touches[0].clientY;
          acc = 0;
        }
      },
      { passive: true },
    );
    term.element.addEventListener(
      "touchmove",
      (ev) => {
        if (lastY == null || ev.touches.length !== 1) return;
        const y = ev.touches[0].clientY;
        acc += lastY - y;
        lastY = y;
        const h = cellH();
        const lines = Math.trunc(acc / h);
        if (lines !== 0) {
          term.scrollLines(lines);
          acc -= lines * h;
        }
      },
      { passive: true },
    );
    const endTouch = () => {
      lastY = null;
    };
    term.element.addEventListener("touchend", endTouch, { passive: true });
    term.element.addEventListener("touchcancel", endTouch, { passive: true });
  }
  // Crisp GPU rendering; fall back silently if WebGL2 is unavailable/lost.
  loadWebgl(it);
  // The web font loads async — refit/redraw once ready so metrics are right.
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(() => forceFit(it));
  }
  // Keep terminal-relevant shortcuts inside the PTY rather than the browser while
  // the terminal is focused. We preventDefault the browser action and return true so
  // xterm still forwards the key to the PTY. Chrome/Firefox honour preventDefault for
  // most Ctrl-combos (Ctrl+R reload, Ctrl+L address bar, Ctrl+D bookmark, Ctrl+F find,
  // Ctrl+U view-source, Ctrl+S save, Ctrl+P print, …) and for F1–F10, so those pass
  // through to the shell (Ctrl+R reverse-search, Ctrl+U kill-line, F-keys in TUIs, …).
  // The hard-reserved ones (Ctrl+W/T/N = close/new tab, Ctrl+Tab, Ctrl+digit, F11/F12)
  // ignore preventDefault outside a fullscreen Keyboard Lock, so they only reach the
  // shell in fullscreen — see the ⛶ toggle in TerminalView / the kb.lock KEYS below.
  // Carve-outs (NO_GRAB): plain Ctrl+C/Ctrl+V stay SIGINT / ^V (and the mac ⌘ clipboard
  // cases are handled above), and Ctrl +/-/0 keep browser zoom (no PTY meaning).
  const NO_GRAB = new Set(["KeyC", "KeyV", "Minus", "Equal", "Digit0", "NumpadAdd", "NumpadSubtract", "Numpad0"]);
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== "keydown") return true;
    const mod = e.ctrlKey || e.metaKey;
    // Clipboard shortcuts — return false so xterm does NOT also forward them to the
    // PTY. Ctrl+C/V (without shift) are deliberately left alone (SIGINT / ^V).
    if (mod && e.shiftKey && e.code === "KeyC") {
      copySelection(term);
      e.preventDefault();
      return false;
    }
    if (mod && e.shiftKey && e.code === "KeyV") {
      pasteClipboard(term);
      e.preventDefault();
      return false;
    }
    if (e.ctrlKey && !e.shiftKey && e.code === "Insert") {
      copySelection(term);
      e.preventDefault();
      return false;
    }
    if (e.shiftKey && !e.ctrlKey && e.code === "Insert") {
      pasteClipboard(term);
      e.preventDefault();
      return false;
    }
    // macOS conventions: ⌘C copies (only when there's a selection, else fall
    // through), ⌘V pastes. (metaKey is Super on Win/Linux — harmless there.)
    if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyC" && term.hasSelection()) {
      copySelection(term);
      e.preventDefault();
      return false;
    }
    if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyV") {
      pasteClipboard(term);
      e.preventDefault();
      return false;
    }
    // Pass-through: claim the key for the PTY (see NO_GRAB / hard-reserved notes above).
    if (/^F([1-9]|10)$/.test(e.code)) e.preventDefault();
    else if (e.ctrlKey && !e.altKey && !NO_GRAB.has(e.code)) e.preventDefault();
    return true;
  });
  // Engage the Keyboard Lock API while the terminal is focused so that, in
  // fullscreen, browser-reserved combos are delivered here instead of the browser.
  // Outside fullscreen lock() is a harmless no-op. We deliberately do NOT lock
  // Escape, so it still exits fullscreen.
  const kb = (navigator as any).keyboard;
  if (kb && kb.lock && term.textarea) {
    // KeyC/KeyV so that in fullscreen Ctrl+Shift+C reaches us (copy) instead of
    // opening DevTools, and Ctrl+Shift+V reaches us (paste) instead of the browser.
    const KEYS = ["KeyW", "KeyT", "KeyN", "KeyR", "KeyL", "KeyS", "KeyP", "KeyC", "KeyV", "PageUp", "PageDown"];
    term.textarea.addEventListener("focus", () => kb.lock(KEYS).catch(() => {}));
    term.textarea.addEventListener("blur", () => {
      try {
        kb.unlock();
      } catch {}
    });
  }

  // Auto-reconnect on focus: refocusing a pane whose PTY socket dropped re-opens it.
  // Covers both ways a pane regains focus — clicking into the terminal, or it
  // becoming the active pane (which calls focusTerm → focuses this textarea).
  if (term.textarea) {
    term.textarea.addEventListener("focus", () => {
      reconnect(paneId);
      // Universal black-pane recovery. However a visible pane reached a black/stale state —
      // a split-pane mount race that painted into a not-yet-laid-out grid, a canvas whose
      // atlas went stale, a reveal that beat layout — the ONE gesture the user reaches for is
      // to click (focus) the pane. reconnect() only helps a dropped/stalled SOCKET; a
      // black-but-live pane needs a RENDERER repaint, and fitInst()/the ResizeObserver only
      // repaint when the grid SHAPE changes (fit() is a no-op otherwise), so focusing an
      // unchanged-shape black pane used to do nothing — leaving reload as the only cure. Force
      // an unconditional clear+repaint here so one click reliably restores any black pane. Runs
      // only on genuine focus-in (not the 4s poll churn), so it can't cause the flicker 32f1871
      // guarded against, and forceFit no-ops on a hidden pane (zero client rects).
      forceFit(inst(paneId));
      // First focus opens the keyboard (the visualViewport resize will place the
      // prompt); this handles switching panes while the keyboard is already up.
      requestAnimationFrame(() => keepInputVisible(inst(paneId)));
    });
    // Refocus elsewhere / keyboard closing: re-place for the new focus, or clear.
    term.textarea.addEventListener("blur", () => {
      requestAnimationFrame(() => keepInputVisible(focusedInst()));
    });
  }
  setSoftKeyboard(it, false); // no session yet → keep the soft keyboard down

  fitAddon.fit();
  term.onData((d) => it.ws && it.ws.readyState === 1 && it.ws.send(JSON.stringify({ type: "input", data: d })));
  term.onResize(({ cols, rows }) => it.ws && it.ws.readyState === 1 && it.ws.send(JSON.stringify({ type: "resize", cols, rows })));
  observe(it, el);
  // Split-pane mount race: this terminal is created/opened synchronously in a mount
  // effect, before the pane's flex/grid cell has necessarily reached its final size.
  // The fit above then measures a not-yet-settled box, and if the resulting grid equals
  // the settled one, no later fit()/ResizeObserver tick is non-no-op to force a repaint —
  // the pane sits black until it's moved. Force one clear+repaint after layout settles
  // (double rAF: past this frame's paint) to close that window at creation time.
  requestAnimationFrame(() => requestAnimationFrame(() => forceFit(it)));
  return term;
}

// Live WebGL contexts we will hold at once, kept well under the browser's ~16-per-tab
// cap. The cap counts contexts the browser has not yet collected, so our own churn
// (drop/load on hide/reveal/redraw) leaves garbage contexts that still occupy slots —
// budgeting only the ones we KNOW about keeps that headroom. Panes past the budget get
// the canvas renderer, which is only marginally slower and holds no GPU context at all.
const WEBGL_BUDGET = 12;
const webglLive = (): number => {
  let n = 0;
  for (const it of insts.values()) if (it.webgl) n++;
  return n;
};

// loadCanvas puts a pane on xterm's 2D canvas renderer — the fallback for every case
// where WebGL can't or shouldn't be used on a DESKTOP pane (over budget, WebGL2
// missing, context lost). It exists to keep us off xterm's DOM renderer, whose
// WidthCache measures each unique glyph by writing textContent and reading offsetWidth:
// one forced synchronous layout PER GLYPH, and its cache is per-renderer-instance, so
// every renderer rebuild pays it again from cold. Measured under the real stylesheets
// on a 40x80 Japanese screen: 2278ms for 3000 unique glyphs cold vs 1.1ms warm — this
// is the Chrome-only Console freeze (Firefox never lost its context, so it never fell
// back). ASCII alone hides it: the DOM cache is a flat array for charCode < 256.
//
// Touch devices deliberately stay on the DOM renderer: their WebGL grief (loadWebgl)
// was about rebuilt contexts painting black, which is not a thing 2D canvas does — but
// that is an untested change on real phones, and the DOM renderer is honest there
// (small grids, few unique glyphs). Desktop is where this bites.
function loadCanvas(it: Inst) {
  const term = it.term;
  if (!term || it.canvas || coarsePointer()) return;
  try {
    const canvas = new CanvasAddon();
    term.loadAddon(canvas);
    it.canvas = canvas;
  } catch {
    it.canvas = null; // xterm stays on the DOM renderer — slow with CJK, but it paints
  }
}

// dropCanvas removes the 2D fallback. Only ONE renderer addon may be loaded at a time
// (each replaces the active renderer), so WebGL must drop it before taking over.
function dropCanvas(it: Inst) {
  const canvas = it.canvas;
  if (!canvas) return;
  it.canvas = null;
  try {
    canvas.dispose();
  } catch {}
}

// loadWebgl attaches a WebGL renderer to a pane's terminal (no-op when one is already
// live). One WebGL context per VISIBLE terminal — browsers cap ~16 across all tabs, so
// hidden panes give theirs up (hideTerm) to stay off the reclaim radar, and WEBGL_BUDGET
// keeps us clear of the cap on top of that. When WebGL can't be had (over budget, no
// WebGL2, context lost) the pane lands on the canvas renderer rather than xterm's DOM
// renderer — see loadCanvas for why that distinction is the whole point.
//
// On context loss (GPU reset / tab backgrounded / the browser reclaiming the oldest
// context once the cap is hit) we dispose the addon. Disposing alone leaves the grid
// blank — the existing rows aren't marked dirty, so nothing repaints until the next PTY
// write or resize. That's the "pane content sometimes goes blank" symptom. Force a
// refit + full repaint right after dispose so the fallback paints the current screen
// immediately.
function loadWebgl(it: Inst) {
  const term = it.term;
  if (!term || it.webgl) return;
  // Never use the WebGL renderer on a touch device. The context lifecycle this file
  // relies on — drop-while-hidden (hideTerm) + rebuild-on-reveal (revealTerm/
  // redrawVisible), added in the mirror-toggle fix so a hidden canvas the browser
  // silently reclaims can't leave a dead renderer — is what mobile GPUs choke on: the
  // INITIAL context paints (so the terminal "used to work" before that churn existed),
  // but a REBUILT context renders all-black with NO webglcontextlost event, so the DOM
  // fallback never engages and no reconnect/reveal ever recovers it.
  //
  // Gate on coarse pointer (any phone/tablet), NOT a viewport-width phone check: the
  // width test (≤760px) let WebGL back in the moment the phone rotated to landscape
  // (>760px) and it went black again. A black terminal is far worse than losing GPU
  // acceleration on a tablet/touch-laptop — xterm's built-in DOM renderer holds no GPU
  // context (so nothing to lose or rebuild, and it cannot go black) and is plenty fast
  // for a terminal. Desktops (fine pointer) keep WebGL.
  if (coarsePointer()) return;
  // Over budget: take the canvas renderer rather than a 13th context. Creating it
  // anyway is what evicts some OTHER pane's context — including a pane that is
  // visible and has no idea it was robbed (webglLost documents that failure).
  if (webglLive() >= WEBGL_BUDGET) {
    loadCanvas(it);
    return;
  }
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => {
      if (it.webgl === webgl) it.webgl = null;
      try {
        webgl.dispose();
      } catch {}
      // Land on canvas, NOT on xterm's DOM renderer: the repaint below would other-
      // wise measure every glyph on screen through a cold WidthCache (see loadCanvas).
      loadCanvas(it);
      try {
        fitInst(it);
        term.refresh(0, term.rows - 1);
      } catch {}
    });
    dropCanvas(it); // only one renderer addon at a time — WebGL takes over from canvas
    term.loadAddon(webgl);
    it.webgl = webgl;
  } catch {
    it.webgl = null;
    loadCanvas(it); // no WebGL2 in this browser/tab
  }
}

// dropWebgl tears a pane's WebGL renderer down (xterm falls back to whatever is left
// — the canvas addon if one is loaded, otherwise its DOM renderer); the terminal, its
// buffer and its socket are untouched. Deliberately does NOT pull in the canvas
// fallback: hideTerm/evict call this for OFF-SCREEN panes, which must hold no renderer
// resources at all, and the rebuild paths call loadWebgl right after.
function dropWebgl(it: Inst) {
  const webgl = it.webgl;
  if (!webgl) return;
  it.webgl = null;
  try {
    webgl.dispose();
  } catch {}
}

// Force a visible terminal onto a fresh renderer and repaint its full buffer.
// Do not create WebGL contexts for display:none mirror panes: besides being
// useless, hidden contexts increase mobile browsers' reclamation pressure.
function redrawVisible(it: Inst) {
  const el = it.term?.element;
  if (!el || !el.isConnected || el.getClientRects().length === 0) return;
  // Rebuild the context only when it is actually dead. This used to drop+load
  // unconditionally, and it runs on every window focus and visibilitychange for EVERY
  // pane — so ordinary tab switching threw away healthy contexts and asked for new
  // ones. The discarded ones still occupy browser slots until they are collected,
  // which is how a tab reaches the ~16 cap and starts evicting the contexts of panes
  // that never moved (webglLost). A live context needs no rebuild: forceFit below
  // clears and repaints it, which is what the black-pane recovery actually needs.
  if (!it.webgl || webglLost(it)) {
    dropWebgl(it);
    loadWebgl(it);
  }
  // Force a clear+repaint even when the grid shape is unchanged: the renderer may have
  // sat hidden/stale and fit() alone would no-op, leaving the pane black.
  forceFit(it);
}

// hideTerm releases a pane's GPU resources while its container is display:none
// (the mirror/chat is shown in front). A hidden WebGL canvas is exactly what
// browsers silently reclaim under GPU pressure — and a reclaim that never fires
// webglcontextlost (or whose restore never comes because the canvas isn't
// visible) leaves a renderer that looks alive but can never paint again: the
// permanently-black-until-reload pane. Holding no context while hidden removes
// that whole failure class; the DOM renderer that takes over is paused by
// xterm's IntersectionObserver anyway, so writes while hidden stay cheap.
export function hideTerm(paneId: string) {
  const it = inst(paneId);
  if (!it) return;
  dropWebgl(it);
  // The 2D fallback goes too: a hidden pane needs no renderer, and holding it would
  // keep a full-size backing canvas per hidden terminal. What takes over is the DOM
  // renderer, which xterm's IntersectionObserver keeps paused while hidden — so its
  // expensive cold-cache repaint (loadCanvas) never runs; revealTerm re-renders first.
  dropCanvas(it);
}

// revealTerm makes a re-shown terminal paint deterministically instead of hoping
// the canvas still holds last frame's pixels: rebuild the WebGL renderer (fresh
// context; also replaces one whose context died without our handler firing),
// refit for the now-laid-out size, and mark every row dirty so the current
// screen is repainted from the buffer. Must run after layout (the caller defers
// past the un-hide, e.g. via rAF). Complements ensureAttached, which guards the
// socket side of a reveal — this guards the renderer side.
export function revealTerm(paneId: string) {
  const it = inst(paneId);
  if (!it || !it.term) return;
  // Detect a context that was lost while we weren't looking (webglLost — same
  // guarded reach-in) so the rebuild below starts from a clean slate.
  if (it.webgl && webglLost(it)) dropWebgl(it);
  loadWebgl(it);
  // Unconditional clear+repaint (not just fit+refresh): a reveal whose grid is already
  // the right shape must still force the renderer to redraw, or it stays black.
  forceFit(it);
}

// observe attaches a ResizeObserver to the pane container so the grid refits when
// the split divider drags the pane (which does NOT fire a window resize event).
function observe(it: Inst, el: HTMLElement) {
  if (!el || typeof ResizeObserver === "undefined") return;
  if (it.ro) it.ro.disconnect();
  it.ro = new ResizeObserver(() => fitInst(it));
  it.ro.observe(el);
}

// sendInput pushes a raw byte sequence to a pane's PTY exactly like a typed key (no
// bracketed-paste wrapping), so the mobile control-key strip can emit Esc / arrows
// / Ctrl-C / Enter. We deliberately do NOT focus the terminal: input goes straight
// over the socket, so focusing would only summon the soft keyboard (Gboard) when
// it's closed. The strip's buttons preventDefault on mousedown, so an already-open
// keyboard keeps its focus and stays up.
export function sendInput(paneId: string, data: string) {
  const it = inst(paneId);
  if (it && it.ws && it.ws.readyState === 1) it.ws.send(JSON.stringify({ type: "input", data }));
}

export function fit(paneId: string) {
  fitInst(inst(paneId));
}

// repaint forces a visible pane's grid to re-sync AND fully repaint (forceFit), regardless of
// whether the grid shape changed. This is the black-pane recovery the plain fit() can't do
// (fit() no-ops on an unchanged shape). Called when a pane becomes ACTIVE — on touch,
// focusTerm() is a no-op so the focus-handler repaint never fires, and the mount-race "never
// painted" black is renderer-independent (it hits the DOM renderer too), so activating a pane
// must repaint it directly. No-op on a hidden pane (forceFit bails on zero client rects).
export function repaint(paneId: string) {
  forceFit(inst(paneId));
}

// setTermBackground tints a pane's terminal background (per session kind / SSM host).
// Spreads the current theme so only the background changes; no-op before the term is
// created.
export function setTermBackground(paneId: string, bg: string) {
  const it = inst(paneId);
  if (!it || !it.term) return;
  const cur = it.term.options.theme || {};
  if (cur.background === bg) return;
  it.term.options.theme = { ...cur, background: bg };
}

// isEditable reports whether an element is a text-entry field the user could be
// typing into (a chat composer, a rename input, …). Used to keep the terminal's
// auto-focus from yanking focus out from under active input.
function isEditable(el: Element | null): boolean {
  if (!el) return false;
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  return (el as HTMLElement).isContentEditable === true;
}

// focusTerm moves keyboard focus into a pane's terminal so the user can type right
// after launching/attaching a session without clicking first. On touch devices this
// is a no-op: auto-focusing xterm's textarea would pop the on-screen keyboard just
// from switching to read a terminal. There the user taps the terminal to focus (and
// summon the keyboard) when they actually want to type.
export function focusTerm(paneId: string) {
  if (coarsePointer()) return;
  const it = inst(paneId);
  // Never steal focus away from another editable element the user is typing in
  // (a chat composer, a rename field). Auto-focus is only a convenience for
  // "type right after launch" — a redundant attach/re-render (e.g. the 4s sessions
  // poll) must not interrupt input elsewhere. Re-focusing THIS terminal's own
  // textarea is fine (it's already where the user is), so only bail for a
  // DIFFERENT editable element.
  const ae = document.activeElement;
  if (ae && ae !== it?.term?.textarea && isEditable(ae)) return;
  try {
    it && it.term && it.term.focus();
  } catch {}
}

// --- round-trip measurement -------------------------------------------------
//
// The terminal's app-level ping/pong is also the ONLY honest probe of how far away
// the PTY is: the ping travels the exact path a keystroke does (browser → CP proxy →
// Agent) and the Agent answers it from the same goroutine that writes PTY output, so
// the round trip covers everything except the PTY/tmux itself (measured at ~0.4 ms in
// the container — i.e. everything that is left IS this number). Without it, "typing
// feels slow" has no observable quantity anywhere in the product, and an investigation
// can only guess between the browser, the relay and the terminal.
//
// One ping is in flight at a time: the Agent's pong is a constant frame
// (`{"type":"pong"}` — workspace/agent/terminal.go) and carries no token to correlate
// with, so a second outstanding ping could not be told apart. A ping sent while one is
// still outstanding is therefore skipped, which is itself the right behaviour: a socket
// that owes us a pong is stalled, and piling on more says nothing new.
export interface RttStats {
  last: number; // newest sample (ms)
  med: number; // median of the retained window
  max: number; // worst of the retained window
  n: number; // samples retained
  at: number; // Date.now() of the newest sample
}

const RTT_LOG_MAX = 24; // ~2 min of history at RTT_INTERVAL
const RTT_INTERVAL = 5000; // sampling cadence (independent of the 15s heartbeat)

function rttStats(it: Inst): RttStats | null {
  const log = it.rttLog;
  if (!log || log.length === 0) return null;
  const sorted = [...log].sort((a, b) => a - b);
  return {
    last: log[log.length - 1],
    med: sorted[Math.floor(sorted.length / 2)],
    max: sorted[sorted.length - 1],
    n: log.length,
    at: it.rttAt ?? 0,
  };
}

// onTermRtt subscribes a pane's round-trip readout. Fires immediately with the
// current value (null before the first sample) and on every sample after that.
export function onTermRtt(paneId: string, fn: (s: RttStats | null) => void) {
  let it = insts.get(paneId);
  if (!it) {
    it = { sessionListeners: new Set(), session: null };
    insts.set(paneId, it);
  }
  if (!it.rttListeners) it.rttListeners = new Set();
  it.rttListeners.add(fn);
  fn(rttStats(it));
  // Explicitly void: React's effect cleanup rejects a returned value, and
  // Set.delete's boolean would otherwise leak through.
  return () => {
    it.rttListeners?.delete(fn);
  };
}

export function terminalRtt(paneId: string): RttStats | null {
  const it = inst(paneId);
  return it ? rttStats(it) : null;
}

// terminalRttAll is the devtools entry point (service.ts): every pane's current
// readout keyed by pane id, so the ids probeTerminalRtt needs can be discovered
// without reaching into the layout store.
export function terminalRttAll(): Record<string, { session: string | null; rtt: RttStats | null }> {
  const out: Record<string, { session: string | null; rtt: RttStats | null }> = {};
  for (const [id, it] of insts) out[id] = { session: it.session ?? null, rtt: rttStats(it) };
  return out;
}

// sendPing marks the send time and pushes the frame. No-op when a pong is still
// outstanding (see above) or the socket isn't open.
function sendPing(it: Inst) {
  const ws = it.ws;
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  if (it.pingAt !== undefined) return;
  it.pingAt = performance.now();
  try {
    ws.send(JSON.stringify({ type: "ping" }));
  } catch {
    it.pingAt = undefined;
  }
}

// noteRtt records the round trip for the outstanding ping. A pong with nothing
// outstanding (a stale socket's late reply, or a burst probe's own) is ignored here.
function noteRtt(it: Inst) {
  const sent = it.pingAt;
  if (sent === undefined) return;
  it.pingAt = undefined;
  const rtt = performance.now() - sent;
  if (!it.rttLog) it.rttLog = [];
  it.rttLog.push(rtt);
  if (it.rttLog.length > RTT_LOG_MAX) it.rttLog.shift();
  it.rttAt = Date.now();
  const waiter = it.pongWaiters && it.pongWaiters.shift();
  if (waiter) waiter(rtt);
  if (it.rttListeners) {
    const s = rttStats(it);
    for (const fn of it.rttListeners) fn(s);
  }
}

function clearRtt(it: Inst) {
  if (it.rttTimer !== undefined) {
    clearInterval(it.rttTimer);
    it.rttTimer = undefined;
  }
  it.pingAt = undefined;
  // A fresh socket starts a fresh measurement: samples from the previous
  // connection describe a path that no longer exists.
  it.rttLog = undefined;
  it.rttAt = undefined;
  it.pongWaiters = undefined;
  if (it.rttListeners) for (const fn of it.rttListeners) fn(null);
}

// probeTerminalRtt takes `n` back-to-back samples and resolves with their spread —
// the thing to run AT THE MOMENT typing feels slow, when the 5 s background cadence
// is too coarse to characterise it. Sequential by necessity (one ping in flight), so
// it also reproduces the serialisation a burst of keystrokes would see.
export async function probeTerminalRtt(paneId: string, n = 15, gapMs = 100): Promise<RttStats | null> {
  const it = inst(paneId);
  if (!it || !it.ws || it.ws.readyState !== WebSocket.OPEN) return null;
  const got: number[] = [];
  for (let i = 0; i < n; i++) {
    const rtt = await new Promise<number | null>((resolve) => {
      const timer = setTimeout(() => {
        // Drop our waiter so a late pong doesn't resolve the NEXT sample's promise
        // with this one's round trip.
        if (it.pongWaiters) it.pongWaiters = it.pongWaiters.filter((w) => w !== waiter);
        resolve(null);
      }, 5000);
      const waiter = (v: number) => {
        clearTimeout(timer);
        resolve(v);
      };
      if (!it.pongWaiters) it.pongWaiters = [];
      it.pongWaiters.push(waiter);
      sendPing(it);
    });
    if (rtt !== null) got.push(rtt);
    if (gapMs > 0) await new Promise((r) => setTimeout(r, gapMs));
  }
  if (got.length === 0) return null;
  const sorted = [...got].sort((a, b) => a - b);
  return {
    last: got[got.length - 1],
    med: sorted[Math.floor(sorted.length / 2)],
    max: sorted[sorted.length - 1],
    n: got.length,
    at: Date.now(),
  };
}

// A PTY socket can die WITHOUT a close frame — a flaky network, a laptop sleep, or a
// proxy dropping an idle connection can leave the WebSocket wedged in OPEN with no
// data and no onclose. The terminal then looks attached but is dead until a full page
// reload. An app-level heartbeat closes that gap: the client pings over the DATA
// channel (the only thing the Control-Plane proxy relays end-to-end — it re-frames and
// swallows protocol ping/pong) and the Agent echoes a pong. Miss enough pongs and we
// treat the socket as dead and re-attach in place, exactly what a reload does.
const HB_INTERVAL = 15000; // ping cadence
const HB_TIMEOUT = 45000; // no pong for this long ⇒ dead → re-attach

// A PTY upgrade still in CONNECTING this long after we opened it is wedged, not
// in-flight — the CP accepted the socket while the session was still booting but
// never completed the /ws/pty handshake, and CONNECTING never fires onclose/onerror.
// Past this window we stop trusting it and re-attach (see connStalled / reconnect /
// ensureAttached). A normal connect reaches OPEN well under a second, so this only
// ever trips on a genuinely stuck one.
const CONNECT_STALL = 3000;

// Close reason the Agent sends when the session is not running (see
// workspace/agent/terminal.go). A browser cannot read the status of a REFUSED
// upgrade, so the reason on an ACCEPTED-then-closed socket is the only way this
// layer can tell "the session ended" from "the connection broke".
const PTY_CLOSE_SESSION_STOPPED = "session stopped";

// connStalled reports a socket wedged in CONNECTING past CONNECT_STALL — the
// "起動直後の黒ターミナル" that no drop/heartbeat path catches (no close frame ever
// arrives). Both the focus/active reconnect and the reveal retry ladder use it to
// decide a hung connect must be torn down and re-opened.
function connStalled(it: Inst | null | undefined) {
  return (
    !!it &&
    !!it.ws &&
    it.ws.readyState === WebSocket.CONNECTING &&
    Date.now() - (it.connAt ?? 0) >= CONNECT_STALL
  );
}

function clearHeartbeat(it: Inst) {
  if (it.hb !== undefined) {
    clearInterval(it.hb);
    it.hb = undefined;
  }
  // Every caller here means "this socket is done" (replaced, closed, disposed), and
  // the round-trip readout describes that socket's path — so it goes with it.
  clearRtt(it);
}

function clearConnWd(it: Inst) {
  if (it.connWd !== undefined) {
    clearTimeout(it.connWd);
    it.connWd = undefined;
  }
}

// startHeartbeat begins pinging on `ws` and re-attaches the pane if pongs stop. Bound
// to a specific socket so a timer left over from a replaced socket no-ops itself.
function startHeartbeat(paneId: string, ws: WebSocket) {
  const it0 = inst(paneId);
  if (!it0) return;
  clearHeartbeat(it0); // also resets the round-trip window (clearRtt)
  it0.lastPong = Date.now();
  // Sample the round trip on its own, faster cadence: the 15 s heartbeat is a
  // liveness contract (HB_TIMEOUT) and must not be sped up just to get numbers, but
  // three samples a minute is too coarse to characterise a stall someone is watching
  // happen. Ping once immediately so a freshly attached pane has a figure to show.
  sendPing(it0);
  it0.rttTimer = setInterval(() => {
    const it = inst(paneId);
    if (!it || it.ws !== ws) return; // socket was replaced — this timer is stale
    sendPing(it);
  }, RTT_INTERVAL);
  it0.hb = setInterval(() => {
    const it = inst(paneId);
    if (!it || it.ws !== ws) return; // socket was replaced — this timer is stale
    if (ws.readyState !== WebSocket.OPEN) return;
    if (Date.now() - (it.lastPong ?? 0) > HB_TIMEOUT) {
      // Pongs stopped: the connection is a zombie. Re-attach (closes this socket and
      // opens a fresh one, replaying the tmux screen) rather than waiting on a TCP
      // timeout that may never come.
      if (it.session) attach(paneId, it.session);
      return;
    }
    // One ping in flight at a time (sendPing): a tick skipped because the socket
    // still owes us a pong is exactly the stall HB_TIMEOUT above is watching for.
    sendPing(it);
    // Watchdog repaint for visible-but-INACTIVE panes. A WebGL canvas can end up
    // showing a stale/garbled composite after the boot churn (multi-pane attach +
    // history replay + layout/font settling), and every existing recovery hook —
    // focus, activation, mirror⇄terminal reveal — requires the user to touch THAT
    // pane; an idle session pane they are merely watching repaints on nothing and
    // stays corrupted indefinitely (confirmed live: clicking the pane fixed it).
    // refresh() redraws every cell from the buffer OVER the canvas without
    // clear(), so there is no blank frame on either renderer (the 7aff545 DOM
    // lesson: clear()+deferred-repaint is the hazard, refresh alone is safe) —
    // at the 15s heartbeat cadence this is imperceptible. Hidden panes are
    // skipped (zero client rects); dead sockets have no heartbeat and read-only
    // history panes are one-shot by design (130b187), so live panes only.
    try {
      const el = it.term?.element;
      if (el && el.isConnected && el.getClientRects().length > 0) {
        // A silently-dead WebGL context freezes the canvas and refresh() draws
        // into the void — heavy mirror⇄terminal switching can evict a visible
        // pane's context without any event (browser cap ~16). Detect it and let
        // forceFit rebuild the renderer.
        if (it.webgl && webglLost(it)) forceFit(it);
        else {
          // Glyph-atlas corruption survives a plain refresh: every cell is
          // redrawn, but FROM poisoned textures, so the garble persists
          // (confirmed live — click/forceFit incl. clearTextureAtlas healed
          // what the refresh-only watchdog could not). Rebuild the atlas before
          // repainting; unlike _renderService.clear() this never blanks the
          // canvas, so the tick stays flicker-free. The DOM renderer has no
          // atlas — refresh alone remains the whole repaint there (7aff545). The 2D
          // canvas fallback keeps an atlas too, so it gets the same treatment.
          /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
          if (it.webgl || it.canvas) (it.term as any).clearTextureAtlas?.();
          it.term!.refresh(0, it.term!.rows - 1);
        }
      }
    } catch {}
  }, HB_INTERVAL);
}

// attach opens a fresh WebSocket to the session's PTY for a pane, replacing any
// current one on that pane.
export function attach(paneId: string, session: string) {
  const it = inst(paneId);
  if (!it || !it.term) return; // ensureTerm must have run (pane mounted)
  // Detach the old socket's handlers before closing it: an intentional switch
  // must not fire its onclose (which would flash "[disconnected]" over the
  // freshly-reset terminal). Only an unexpected server-side drop should show it.
  if (it.ws) {
    it.ws.onclose = null;
    it.ws.onmessage = null;
    it.ws.onerror = null; // a late error on the old socket must not flag the new one
    it.ws.close();
  }
  clearHeartbeat(it); // stop the old socket's heartbeat before opening the new one
  clearConnWd(it); // and the old socket's connect-stall watchdog
  it.term.reset();
  setSession(it, session);
  it.dropped = false; // fresh socket: clear any prior unexpected-drop flag
  it.rx = false; // fresh socket: no PTY bytes yet (ensureAttached watches this)
  setSoftKeyboard(it, false); // keep the keyboard down until the PTY is live
  const ws = new WebSocket(wsURL(session));
  it.ws = ws;
  it.connAt = Date.now();
  ws.binaryType = "arraybuffer";
  // Connect-stall watchdog. A PTY upgrade that never completes stays CONNECTING with
  // no close frame — onopen/onclose/onerror and the heartbeat all never fire — so an
  // idle/off-screen pane (whose focus/active reconnect never triggers) would sit black
  // forever. If this socket is still CONNECTING past CONNECT_STALL, tear it down and
  // re-attach: a fresh connect, by when the session has finished booting, completes and
  // draws. attach() schedules a new watchdog, so it keeps retrying until the PTY opens.
  // A healthy connect reaches onopen first and clears this.
  it.connWd = setTimeout(() => {
    const cur = inst(paneId);
    if (cur && cur.ws === ws && ws.readyState === WebSocket.CONNECTING && cur.session) {
      attach(paneId, cur.session);
    }
  }, CONNECT_STALL + 500);
  ws.onopen = () => {
    clearConnWd(it); // connected → the stall watchdog is done
    fitInst(it); // size the grid before reporting it to the PTY (next line)
    setSoftKeyboard(it, true); // PTY connected → allow the soft keyboard for input
    ws.send(JSON.stringify({ type: "resize", cols: it.term!.cols, rows: it.term!.rows }));
    startHeartbeat(paneId, ws); // begin liveness pinging on this socket
    // A fresh attach into a split pane can win the race against the pane's layout, so
    // the tmux replay paints into a grid that's the right shape but never repainted —
    // a black pane no reconnect fixes. Force a clear+repaint after layout settles.
    requestAnimationFrame(() => forceFit(it));
  };
  ws.onmessage = (ev) => {
    const d = ev.data;
    if (typeof d === "string") {
      // Text frames are out-of-band control (heartbeat), never terminal output — PTY
      // output is always binary. Consume the pong without writing it to the grid.
      try {
        if (JSON.parse(d)?.type === "pong") {
          it.lastPong = Date.now();
          noteRtt(it);
        }
      } catch {}
      return;
    }
    if (d instanceof ArrayBuffer) {
      it.rx = true; // real PTY output arrived — the attach painted, not a blank socket
      it.term!.write(new Uint8Array(d));
    }
  };
  // A socket error usually precedes an unclean drop — flag it so a refocus reconnects
  // even if onclose never arrives (the heartbeat is the backstop when neither fires).
  ws.onerror = () => {
    it.dropped = true;
  };
  // An unexpected server-side drop (intentional switches/detach null this handler
  // first). Flag it so refocusing the pane reconnects — see reconnect().
  ws.onclose = (ev) => {
    clearConnWd(it); // socket resolved (closed) → the connect-stall watchdog is moot
    // A finite history replay closes normally after its last frame. Keep the
    // rendered scrollback and do not label that expected EOF as a disconnection.
    if (ev.code === 1000) {
      clearHeartbeat(it);
      setSoftKeyboard(it, false);
      // The Agent closes a STOPPED session's replay with this reason (wire contract:
      // ptyCloseSessionStopped in workspace/agent/terminal.go, forwarded verbatim by
      // the CP proxy). Say so in the grid: a pane that is simply blank — the normal
      // case once a container restart has emptied the /tmp history ring — reads as a
      // broken terminal, and the user has no reason to hunt for the 再開 chip.
      if (ev.reason === PTY_CLOSE_SESSION_STOPPED) {
        it.term!.write("\r\n" + tr("onb.term_session_stopped") + "\r\n");
      }
      return;
    }
    it.dropped = true;
    clearHeartbeat(it); // socket gone → stop pinging
    setSoftKeyboard(it, false); // no live PTY → don't summon the keyboard on focus
    it.term!.write("\r\n" + tr("onb.term_disconnected") + "\r\n");
    // The raw WebSocket bypasses the fetch wrapper, so an auth-expiry drop (401 on the
    // upgrade) is indistinguishable from an ordinary network drop here. Probe a cheap
    // API call once: a 401 trips the wrapper's auth-expired latch (→ re-login dialog),
    // so a terminal-only view prompts re-login instead of sitting on [disconnected];
    // any other outcome is a plain drop that reconnect-on-focus already handles.
    if (!isAuthExpired()) void fetch(rel("api/whoami")).catch(() => {});
  };
  // Focus after the next paint, by when the pane has been un-hidden (the caller
  // flips state in the same React batch). So the user can type immediately after
  // launching a session.
  requestAnimationFrame(() => focusTerm(paneId));
}

// reconnect re-opens a pane's PTY socket if it dropped unexpectedly OR wedged in a
// stalled CONNECTING (see connStalled). No-op when the pane has no session, or holds
// a healthy socket (open, or an in-flight connect still within the stall window).
// Wired to the terminal's focus so returning to a dead/black pane — by clicking it or
// making it the active pane, and via the global focus/visibility recovery — brings the
// session back. For a genuine unexpected drop we do this on focus rather than instantly
// so the pane stays put (and its "[disconnected]" notice readable) until the user comes
// back to it; a stalled connect shows no notice, so recovering it is pure upside.
export function reconnect(paneId: string) {
  const it = inst(paneId);
  if (!it || !it.term || !it.session) return;
  const stalled = connStalled(it);
  // Nothing to recover unless the socket dropped or its connect has wedged.
  if (!it.dropped && !stalled) return;
  // Leave a still-healthy socket alone — but a stalled CONNECTING is NOT healthy, so
  // it falls through to a fresh attach even though its readyState is CONNECTING.
  if (!stalled && it.ws && (it.ws.readyState === WebSocket.CONNECTING || it.ws.readyState === WebSocket.OPEN)) return;
  attach(paneId, it.session);
}

// ensureAttached guarantees a live PTY socket to `session` for a pane, then refits it.
// Used when a terminal is REVEALED (toggled back from the chat mirror / a split pane
// un-hidden): if the pane is already connected to this session, just refit for the now-
// laid-out size (scrollback preserved); otherwise (no socket, a socket wedged closed, or
// one left over from a session this reused pane previously showed) attach afresh. This
// is what recovers the "attached while hidden / reused pane" cases that otherwise only a
// full page reload cleared.
export function ensureAttached(paneId: string, session: string) {
  const it = inst(paneId);
  if (!it || !it.term || !session) return;
  // A socket wedged OPEN but with no PTY byte ever received is one "起動直後の黒
  // ターミナル": the fresh attach raced the session bring-up and produced no draw,
  // yet the socket looks healthy (heartbeat pongs keep flowing regardless of PTY
  // output), so neither the drop-flag reconnect nor the zombie heartbeat fires and
  // it sits blank until a full reload. Treat OPEN-but-never-drew as NOT live so the
  // retry re-attaches and repaints.
  //
  // A CONNECTING socket is normally left alone — a genuinely in-flight connect will
  // draw shortly. But right after a session launches, the PTY upgrade can hang in
  // CONNECTING (the CP accepts the socket while the agent/tmux is still coming up but
  // never completes the /ws/pty handshake). That is the OTHER "起動直後の黒" and the
  // nastier one: CONNECTING never fires onclose/onerror, so the drop-flag reconnect
  // and heartbeat never trip, and — if we blindly trust CONNECTING — every retry rung
  // (and the reveal re-check) skips it, leaving the pane black until a focus tap or
  // reload. So only a FRESH connect (CONNECTING within CONNECT_STALL) counts as live;
  // a stalled one is re-attached (a new connect, by when the agent is up, draws).
  const rs = it.ws && it.ws.readyState;
  const live =
    it.session === session &&
    !!it.ws &&
    ((rs === WebSocket.CONNECTING && !connStalled(it)) || (rs === WebSocket.OPEN && it.rx === true));
  if (live) {
    // Receiving PTY bytes proves the socket side is healthy, but it does NOT prove
    // those bytes reached the screen (a split-pane mount race can leave the grid
    // shaped right yet never painted, and a stale glyph atlas can garble it). The
    // attach retry ladder doubles as the startup-render watchdog, so repaint the
    // buffer via forceFit (grid re-sync + atlas rebuild + full refresh). We do NOT
    // rebuild the WebGL renderer here: this path also runs on every routine re-verify
    // (incl. a redundant re-render from the 4s sessions poll), and a renderer swap
    // each time is a visible flicker. A genuinely dead-but-not-lost WebGL context
    // (rare; the "black until reload" case) is rebuilt on the explicit recovery
    // gestures instead — reconnectSession (re-clicking the session) and the
    // visibility/focus recovery — via redrawVisible. forceFit no-ops on a hidden
    // mirror pane (zero client rects), same as redrawVisible.
    forceFit(it);
    return;
  }
  attach(paneId, session);
}

// reconnectSession revives any pane currently showing `name` whose PTY dropped. Used
// when the user re-clicks an already-open but disconnected session in the list: that
// doesn't change the pane's props, so the declarative attach and the active/focus
// reconnect paths don't fire. No-op on panes that aren't dropped.
export function reconnectSession(name: string) {
  if (!name) return;
  for (const [id, it] of insts) {
    if (it && it.session === name) {
      reconnect(id);
      // Re-clicking an open session is an explicit recovery gesture. A mobile
      // WebGL canvas can be black while its PTY socket is still healthy, so the
      // renderer must be recovered independently of reconnect().
      redrawVisible(it);
    }
  }
}

export function detach(paneId: string) {
  const it = inst(paneId);
  if (!it) return;
  clearHeartbeat(it);
  clearConnWd(it);
  if (it.ws) {
    try {
      it.ws.onclose = null; // intentional detach — don't print "[disconnected]"
      it.ws.onerror = null;
      it.ws.close();
    } catch {}
    it.ws = null;
  }
  setSoftKeyboard(it, false); // empty pane → keep the soft keyboard down
  setSession(it, null);
}

// clearTerm wipes a pane's scrollback + screen without tearing the xterm down. Used
// when a pane goes empty (its session/chat/file was closed but the pane — and its
// xterm — is kept as an empty terminal): the reused instance would otherwise still show
// the old session's output. Detach nulls the socket but deliberately preserves the
// buffer (a stopped session stays readable), so the wipe is a separate, explicit step.
export function clearTerm(paneId: string) {
  const it = inst(paneId);
  try {
    it && it.term && it.term.reset();
  } catch {}
}

// disposeTerm tears a pane's terminal down entirely: closes the socket, disposes the
// xterm instance and its ResizeObserver, and forgets the pane. Called when a split
// pane is closed / replaced by a non-terminal view / wiped on tenant switch.
export function disposeTerm(paneId: string) {
  const it = inst(paneId);
  if (!it) return;
  clearHeartbeat(it);
  clearConnWd(it);
  if (it.ws) {
    try {
      it.ws.onclose = null;
      it.ws.onmessage = null;
      it.ws.onerror = null;
      it.ws.close();
    } catch {}
    it.ws = null;
  }
  if (it.ro) {
    try {
      it.ro.disconnect();
    } catch {}
    it.ro = null;
  }
  if (it.term) {
    try {
      it.term.dispose(); // also disposes loaded addons (webgl/canvas included)
    } catch {}
    it.term = null;
    // Clear BOTH renderer handles: webglLive() counts them to budget contexts, so a
    // stale handle on a disposed terminal would permanently shrink the budget.
    it.webgl = null;
    it.canvas = null;
  }
  setSession(it, null);
  insts.delete(paneId);
}

// sessionOf returns the session name currently attached to a pane (or null).
export function sessionOf(paneId: string) {
  const it = inst(paneId);
  return it ? it.session ?? null : null;
}

// keepOnly disposes every terminal whose pane id is not in `ids` — the reconciler
// for layout changes (pane closed, browser back/forward dropping a pane, tenant
// switch wiping all panes). Prevents orphaned xterm instances + WebSockets.
export function keepOnly(ids: string[]) {
  const keep = new Set(ids);
  for (const id of [...insts.keys()]) {
    if (!keep.has(id)) disposeTerm(id);
  }
}
