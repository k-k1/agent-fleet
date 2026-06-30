// xterm.js terminal manager. Terminals live outside React so each instance and its
// WebSocket survive view switches (a pane's DOM container stays mounted and we just
// open() the term into it once). Originally a module singleton; now keyed by paneId
// so the console can show several sessions side by side (split panes). Ported from
// legacy-phase1/app.js.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "./api.js";
import { getSettings, subscribe as subscribeSettings, fontStack } from "./lib/settings.js";

// One entry per pane. { term, fitAddon, ws, session, sessionListeners, ro }.
const insts = new Map();
function inst(paneId) {
  return insts.get(paneId) || null;
}

// listeners notified when a pane's attached session name changes (so the UI can
// show it). Registered per pane; survive across ensureTerm calls.
export function onSession(paneId, fn) {
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
function setSession(it, name) {
  it.session = name;
  for (const fn of it.sessionListeners) fn(name);
}

// Clipboard helpers. Browsers route Ctrl+C/Ctrl+V inside a focused terminal to the
// PTY (SIGINT / literal ^V), NOT the system clipboard — so plain copy/paste never
// worked. We wire explicit gestures (copy-on-select, Ctrl+Shift+C / Ctrl+Insert,
// right/middle-click paste, Shift+Insert / Ctrl+Shift+V) to the async Clipboard API,
// leaving Ctrl+C free to interrupt the foreground program.
function copySelection(term) {
  const sel = term && term.getSelection();
  if (sel && navigator.clipboard) navigator.clipboard.writeText(sel).catch(() => {});
}
function pasteClipboard(term) {
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
  if (window.visualViewport) window.visualViewport.addEventListener("resize", fitAll);
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

function fitInst(it) {
  try {
    it && it.fitAddon && it.fitAddon.fit();
  } catch {}
}

// ensureTerm builds a pane's terminal once and opens it into `el`. Subsequent calls
// for the same pane re-open into the element if React remounted the container; the
// instance (and scrollback) persists.
export function ensureTerm(paneId, el) {
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
    let lastY = null,
      acc = 0;
    const cellH = () => {
      const vp = term.element.querySelector(".xterm-viewport");
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
  // the terminal is focused. preventDefault stops the browser default for the
  // combos browsers *allow* us to (Ctrl+S/P/…). The hard-reserved ones (Ctrl+W/T/N
  // = close/new tab) only yield to the page while it holds a Keyboard Lock in
  // fullscreen — see the ⛶ toggle in TerminalView. We still return true so xterm
  // forwards the key to the PTY (e.g. Ctrl+W = delete-word in the shell).
  const GRAB = new Set(["KeyW", "KeyT", "KeyN", "KeyS", "KeyP"]);
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
    if (mod && !e.altKey && GRAB.has(e.code)) e.preventDefault();
    return true;
  });
  // Engage the Keyboard Lock API while the terminal is focused so that, in
  // fullscreen, browser-reserved combos are delivered here instead of the browser.
  // Outside fullscreen lock() is a harmless no-op. We deliberately do NOT lock
  // Escape, so it still exits fullscreen.
  const kb = navigator.keyboard;
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
    term.textarea.addEventListener("focus", () => reconnect(paneId));
  }

  fitAddon.fit();
  term.onData((d) => it.ws && it.ws.readyState === 1 && it.ws.send(JSON.stringify({ type: "input", data: d })));
  term.onResize(({ cols, rows }) => it.ws && it.ws.readyState === 1 && it.ws.send(JSON.stringify({ type: "resize", cols, rows })));
  observe(it, el);
  return term;
}

// observe attaches a ResizeObserver to the pane container so the grid refits when
// the split divider drags the pane (which does NOT fire a window resize event).
function observe(it, el) {
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
export function sendInput(paneId, data) {
  const it = inst(paneId);
  if (it && it.ws && it.ws.readyState === 1) it.ws.send(JSON.stringify({ type: "input", data }));
}

export function fit(paneId) {
  fitInst(inst(paneId));
}

// focusTerm moves keyboard focus into a pane's terminal so the user can type right
// after launching/attaching a session without clicking first.
export function focusTerm(paneId) {
  const it = inst(paneId);
  try {
    it && it.term && it.term.focus();
  } catch {}
}

// attach opens a fresh WebSocket to the session's PTY for a pane, replacing any
// current one on that pane.
export function attach(paneId, session) {
  const it = inst(paneId);
  if (!it || !it.term) return; // ensureTerm must have run (pane mounted)
  // Detach the old socket's handlers before closing it: an intentional switch
  // must not fire its onclose (which would flash "[disconnected]" over the
  // freshly-reset terminal). Only an unexpected server-side drop should show it.
  if (it.ws) {
    it.ws.onclose = null;
    it.ws.onmessage = null;
    it.ws.close();
  }
  it.term.reset();
  setSession(it, session);
  it.dropped = false; // fresh socket: clear any prior unexpected-drop flag
  const ws = new WebSocket(wsURL(session));
  it.ws = ws;
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    fitInst(it);
    ws.send(JSON.stringify({ type: "resize", cols: it.term.cols, rows: it.term.rows }));
  };
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) it.term.write(new Uint8Array(ev.data));
    else it.term.write(ev.data);
  };
  // An unexpected server-side drop (intentional switches/detach null this handler
  // first). Flag it so refocusing the pane reconnects — see reconnect().
  ws.onclose = () => {
    it.dropped = true;
    it.term.write("\r\n[disconnected]\r\n");
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
export function reconnect(paneId) {
  const it = inst(paneId);
  if (!it || !it.term || !it.session || !it.dropped) return;
  if (it.ws && (it.ws.readyState === WebSocket.CONNECTING || it.ws.readyState === WebSocket.OPEN)) return;
  attach(paneId, it.session);
}

export function detach(paneId) {
  const it = inst(paneId);
  if (!it) return;
  if (it.ws) {
    try {
      it.ws.onclose = null; // intentional detach — don't print "[disconnected]"
      it.ws.close();
    } catch {}
    it.ws = null;
  }
  setSession(it, null);
}

// disposeTerm tears a pane's terminal down entirely: closes the socket, disposes the
// xterm instance and its ResizeObserver, and forgets the pane. Called when a split
// pane is closed / replaced by a non-terminal view / wiped on tenant switch.
export function disposeTerm(paneId) {
  const it = inst(paneId);
  if (!it) return;
  if (it.ws) {
    try {
      it.ws.onclose = null;
      it.ws.onmessage = null;
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
export function sessionOf(paneId) {
  const it = inst(paneId);
  return it ? it.session ?? null : null;
}

// keepOnly disposes every terminal whose pane id is not in `ids` — the reconciler
// for layout changes (pane closed, browser back/forward dropping a pane, tenant
// switch wiping all panes). Prevents orphaned xterm instances + WebSockets.
export function keepOnly(ids) {
  const keep = new Set(ids);
  for (const id of [...insts.keys()]) {
    if (!keep.has(id)) disposeTerm(id);
  }
}
