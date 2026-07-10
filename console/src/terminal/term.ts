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
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../core/api/client.ts";
import { getSettings, subscribe as subscribeSettings, fontStack } from "../lib/settings.ts";

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
  lastPong?: number; // ms of the last pong seen on the current socket
  rx?: boolean; // any PTY byte received on the CURRENT socket (see ensureAttached)
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
function pasteClipboard(term: Terminal) {
  if (!term || !navigator.clipboard) return;
  navigator.clipboard
    .readText()
    .then((t) => {
      // term.paste() routes through onData (incl. bracketed-paste wrapping) → PTY.
      if (t) term.paste(t);
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
    it.term.options.fontFamily = fontStack(s.termFont);
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
  const recoverAll = () => {
    for (const id of insts.keys()) reconnect(id);
  };
  window.addEventListener("focus", recoverAll);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") recoverAll();
  });
}

function fitInst(it: Inst | null | undefined) {
  try {
    it && it.fitAddon && it.fitAddon.fit();
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
  const keyboard = window.innerHeight - vv.height; // ~0 unless a soft keyboard is up
  if (keyboard < 150) return; // ignore URL-bar show/hide; only react to a keyboard
  const pane = el.closest(".pane") || el;
  const overlap = pane.getBoundingClientRect().bottom - (vv.offsetTop + vv.height);
  if (overlap > 0) main.style.transform = `translateY(-${Math.ceil(overlap) + 4}px)`;
}

// ensureTerm builds a pane's terminal once and opens it into `el`. Subsequent calls
// for the same pane re-open into the element if React remounted the container; the
// instance (and scrollback) persists.
export function ensureTerm(paneId: string, el: HTMLElement) {
  let it = insts.get(paneId);
  if (it && it.term) {
    // Re-open into the element if React remounted the container.
    if (el && it.term.element?.parentElement !== el) it.term.open(el);
    observe(it, el);
    fitInst(it);
    return it.term;
  }
  // May already hold a placeholder from an early onSession() subscription.
  if (!it) {
    it = { sessionListeners: new Set(), session: null };
    insts.set(paneId, it);
  }
  const s0 = getSettings();
  const term = new Terminal({
    fontSize: s0.termSize,
    fontFamily: fontStack(s0.termFont),
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
  // Crisp GPU rendering; fall back silently if WebGL2 is unavailable/lost. One
  // WebGL context per terminal — browsers cap ~16, so splits stay well within.
  // On context loss (GPU reset / tab backgrounded / the browser reclaiming the
  // oldest context once its ~16 cap is hit across all tabs) we dispose the addon
  // so xterm reverts to the DOM renderer. Disposing alone leaves the grid blank —
  // the existing rows aren't marked dirty, so nothing repaints until the next PTY
  // write or resize. That's the "pane content sometimes goes blank" symptom. Force
  // a refit + full repaint right after dispose so the fallback renderer paints the
  // current screen immediately.
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => {
      try {
        webgl.dispose();
      } catch {}
      try {
        fitInst(it);
        term.refresh(0, term.rows - 1);
      } catch {}
    });
    term.loadAddon(webgl);
  } catch {}
  // The web font loads async — refit/redraw once ready so metrics are right.
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(() => {
      try {
        fitAddon.fit();
        term.refresh(0, term.rows - 1);
      } catch {}
    });
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
  return term;
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

// focusTerm moves keyboard focus into a pane's terminal so the user can type right
// after launching/attaching a session without clicking first. On touch devices this
// is a no-op: auto-focusing xterm's textarea would pop the on-screen keyboard just
// from switching to read a terminal. There the user taps the terminal to focus (and
// summon the keyboard) when they actually want to type.
export function focusTerm(paneId: string) {
  if (coarsePointer()) return;
  const it = inst(paneId);
  try {
    it && it.term && it.term.focus();
  } catch {}
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

function clearHeartbeat(it: Inst) {
  if (it.hb !== undefined) {
    clearInterval(it.hb);
    it.hb = undefined;
  }
}

// startHeartbeat begins pinging on `ws` and re-attaches the pane if pongs stop. Bound
// to a specific socket so a timer left over from a replaced socket no-ops itself.
function startHeartbeat(paneId: string, ws: WebSocket) {
  const it0 = inst(paneId);
  if (!it0) return;
  clearHeartbeat(it0);
  it0.lastPong = Date.now();
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
    try {
      ws.send(JSON.stringify({ type: "ping" }));
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
  it.term.reset();
  setSession(it, session);
  it.dropped = false; // fresh socket: clear any prior unexpected-drop flag
  it.rx = false; // fresh socket: no PTY bytes yet (ensureAttached watches this)
  setSoftKeyboard(it, false); // keep the keyboard down until the PTY is live
  const ws = new WebSocket(wsURL(session));
  it.ws = ws;
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    fitInst(it);
    setSoftKeyboard(it, true); // PTY connected → allow the soft keyboard for input
    ws.send(JSON.stringify({ type: "resize", cols: it.term!.cols, rows: it.term!.rows }));
    startHeartbeat(paneId, ws); // begin liveness pinging on this socket
  };
  ws.onmessage = (ev) => {
    const d = ev.data;
    if (typeof d === "string") {
      // Text frames are out-of-band control (heartbeat), never terminal output — PTY
      // output is always binary. Consume the pong without writing it to the grid.
      try {
        if (JSON.parse(d)?.type === "pong") it.lastPong = Date.now();
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
  ws.onclose = () => {
    it.dropped = true;
    clearHeartbeat(it); // socket gone → stop pinging
    setSoftKeyboard(it, false); // no live PTY → don't summon the keyboard on focus
    it.term!.write("\r\n[disconnected]\r\n");
  };
  // Focus after the next paint, by when the pane has been un-hidden (the caller
  // flips state in the same React batch). So the user can type immediately after
  // launching a session.
  requestAnimationFrame(() => focusTerm(paneId));
}

// reconnect re-opens a pane's PTY socket if it dropped unexpectedly. No-op when the
// pane has no session, is already connecting/open, or wasn't dropped (e.g. an
// intentional detach). Wired to the terminal's focus so returning to a dead pane —
// by clicking it or making it the active pane — brings the session back. We do this
// on focus rather than instantly so a dropped pane stays put (and its
// "[disconnected]" notice readable) until the user comes back to it.
export function reconnect(paneId: string) {
  const it = inst(paneId);
  if (!it || !it.term || !it.session || !it.dropped) return;
  if (it.ws && (it.ws.readyState === WebSocket.CONNECTING || it.ws.readyState === WebSocket.OPEN)) return;
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
  // A socket wedged OPEN but with no PTY byte ever received is the "起動直後の黒
  // ターミナル": the fresh attach raced the session bring-up and produced no draw,
  // yet the socket looks healthy (heartbeat pongs keep flowing regardless of PTY
  // output), so neither the drop-flag reconnect nor the zombie heartbeat fires and
  // it sits blank until a full reload. Treat OPEN-but-never-drew as NOT live so the
  // retry re-attaches and repaints. A CONNECTING socket is left alone (an in-flight
  // connect will draw shortly); by the first retry (1.5s) a healthy attach has long
  // since received tmux's initial redraw, so rx===false here reliably means blank.
  const live =
    it.session === session &&
    it.ws &&
    (it.ws.readyState === WebSocket.CONNECTING ||
      (it.ws.readyState === WebSocket.OPEN && it.rx === true));
  if (live) {
    fitInst(it); // connected → just size the grid to the visible container
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
    if (it && it.session === name) reconnect(id);
  }
}

export function detach(paneId: string) {
  const it = inst(paneId);
  if (!it) return;
  clearHeartbeat(it);
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
      it.term.dispose();
    } catch {}
    it.term = null;
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
