// xterm.js terminal singleton. Kept outside React so the terminal instance and its
// WebSocket survive view switches (the Terminal view's DOM container stays mounted
// and we just open() the term into it once). Ported from legacy-phase1/app.js.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "./api.js";
import { getSettings, subscribe as subscribeSettings, fontStack } from "./lib/settings.js";

let term = null;
let fitAddon = null;
let ws = null;

// listeners notified when the attached session name changes (so the UI can show it)
const sessionListeners = new Set();
let currentSession = null;
export function onSession(fn) {
  sessionListeners.add(fn);
  fn(currentSession);
  return () => sessionListeners.delete(fn);
}
function setSession(name) {
  currentSession = name;
  for (const fn of sessionListeners) fn(name);
}

// Clipboard helpers. Browsers route Ctrl+C/Ctrl+V inside a focused terminal to the
// PTY (SIGINT / literal ^V), NOT the system clipboard — so plain copy/paste never
// worked. We wire explicit gestures (copy-on-select, Ctrl+Shift+C / Ctrl+Insert,
// right/middle-click paste, Shift+Insert / Ctrl+Shift+V) to the async Clipboard API,
// leaving Ctrl+C free to interrupt the foreground program.
function copySelection() {
  const sel = term && term.getSelection();
  if (sel && navigator.clipboard) navigator.clipboard.writeText(sel).catch(() => {});
}
function pasteClipboard() {
  if (!term || !navigator.clipboard) return;
  navigator.clipboard
    .readText()
    .then((t) => {
      // term.paste() routes through onData (incl. bracketed-paste wrapping) → PTY.
      if (t) term.paste(t);
    })
    .catch(() => {});
}

// ensureTerm builds the terminal once and opens it into `el`. Subsequent calls
// with the same element are no-ops; the instance (and scrollback) persists.
export function ensureTerm(el) {
  if (term) {
    // Re-open into the element if React remounted the container (shouldn't happen
    // while the Terminal view stays mounted, but be safe).
    if (el && term.element?.parentElement !== el) term.open(el);
    fit();
    return term;
  }
  const s0 = getSettings();
  term = new Terminal({
    fontSize: s0.termSize,
    fontFamily: fontStack(s0.termFont),
    theme: { background: "#1e1e1e" },
    cursorBlink: true,
    allowProposedApi: true,
  });
  // Apply terminal font/size from settings, live, when the user changes them.
  subscribeSettings(applyTermSettings);
  fitAddon = new FitAddon();
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
      if (ev.button === 0 && term.hasSelection()) copySelection();
    });
    term.element.addEventListener("contextmenu", (ev) => {
      ev.preventDefault();
      pasteClipboard();
    });
    term.element.addEventListener("auxclick", (ev) => {
      if (ev.button === 1) {
        ev.preventDefault();
        pasteClipboard();
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
  // Crisp GPU rendering; fall back silently if WebGL2 is unavailable/lost.
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => webgl.dispose());
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
      copySelection();
      e.preventDefault();
      return false;
    }
    if (mod && e.shiftKey && e.code === "KeyV") {
      pasteClipboard();
      e.preventDefault();
      return false;
    }
    if (e.ctrlKey && !e.shiftKey && e.code === "Insert") {
      copySelection();
      e.preventDefault();
      return false;
    }
    if (e.shiftKey && !e.ctrlKey && e.code === "Insert") {
      pasteClipboard();
      e.preventDefault();
      return false;
    }
    // macOS conventions: ⌘C copies (only when there's a selection, else fall
    // through), ⌘V pastes. (metaKey is Super on Win/Linux — harmless there.)
    if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyC" && term.hasSelection()) {
      copySelection();
      e.preventDefault();
      return false;
    }
    if (e.metaKey && !e.ctrlKey && !e.shiftKey && e.code === "KeyV") {
      pasteClipboard();
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

  fitAddon.fit();
  term.onData((d) => ws && ws.readyState === 1 && ws.send(JSON.stringify({ type: "input", data: d })));
  term.onResize(({ cols, rows }) => ws && ws.readyState === 1 && ws.send(JSON.stringify({ type: "resize", cols, rows })));
  window.addEventListener("resize", fit);
  // On mobile the soft keyboard shrinks the layout viewport rather than firing a
  // window resize; visualViewport fires its own resize so the grid refits and the
  // prompt isn't left hidden behind the keyboard.
  if (window.visualViewport) window.visualViewport.addEventListener("resize", fit);
  return term;
}

// sendInput pushes a raw byte sequence to the PTY exactly like a typed key (no
// bracketed-paste wrapping), so the mobile control-key strip can emit Esc / arrows
// / Ctrl-C / Enter. We deliberately do NOT focus the terminal: input goes straight
// over the socket, so focusing would only summon the soft keyboard (Gboard) when
// it's closed. The strip's buttons preventDefault on mousedown, so an already-open
// keyboard keeps its focus and stays up.
export function sendInput(data) {
  if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: "input", data }));
}

export function fit() {
  try {
    fitAddon && fitAddon.fit();
  } catch {}
}

// applyTermSettings pushes the current font family/size into the live terminal and
// refits so the grid matches the new metrics.
function applyTermSettings() {
  if (!term) return;
  const s = getSettings();
  term.options.fontFamily = fontStack(s.termFont);
  term.options.fontSize = s.termSize;
  fit();
  try {
    term.refresh(0, term.rows - 1);
  } catch {}
}

// focusTerm moves keyboard focus into the terminal so the user can type right
// after launching/attaching a session without clicking first.
export function focusTerm() {
  try {
    term && term.focus();
  } catch {}
}

// attach opens a fresh WebSocket to the session's PTY, replacing any current one.
export function attach(session) {
  if (!term) return; // ensureTerm must have run (Terminal view mounted)
  if (ws) ws.close();
  term.reset();
  setSession(session);
  ws = new WebSocket(wsURL(session));
  ws.binaryType = "arraybuffer";
  ws.onopen = () => {
    fit();
    ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
  };
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
    else term.write(ev.data);
  };
  ws.onclose = () => term.write("\r\n[disconnected]\r\n");
  // Focus after the next paint, by when the terminal view has been un-hidden
  // (showTerminal flips the mode in the same React batch). So the user can type
  // immediately after launching a session.
  requestAnimationFrame(() => focusTerm());
}

export function detach() {
  if (ws) {
    try {
      ws.close();
    } catch {}
    ws = null;
  }
  setSession(null);
}

// reconstructURL rebuilds the /login sign-in URL from the xterm buffer. Ink hard-
// wraps it across rows, so neither plain copy nor web-links yields the whole URL;
// we join full-width rows only on demand (no auto-popup, no false hits).
export function reconstructURL() {
  if (!term) return null;
  const buf = term.buffer.active,
    cols = term.cols;
  for (let y = buf.length - 1; y >= Math.max(0, buf.length - 200); y--) {
    const line = buf.getLine(y);
    if (!line) continue;
    const m = line.translateToString(true).match(/(https:\/\/[^\s]*)$/);
    if (!m) continue; // a URL fragment reaching the row end => wrapped onward
    let url = m[1];
    for (let yy = y + 1; yy < buf.length; yy++) {
      const seg = buf.getLine(yy)?.translateToString(true) ?? "";
      if (!seg || /[^\x21-\x7e]/.test(seg)) break; // non-URL char (incl space) => end
      url += seg;
      if (seg.length < cols) break; // shorter than width => last segment
    }
    if (/oauth|authorize/i.test(url)) return url;
  }
  return null;
}
