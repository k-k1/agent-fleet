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
    if (e.type === "keydown" && (e.ctrlKey || e.metaKey) && !e.altKey && GRAB.has(e.code)) {
      e.preventDefault();
    }
    return true;
  });
  // Engage the Keyboard Lock API while the terminal is focused so that, in
  // fullscreen, browser-reserved combos are delivered here instead of the browser.
  // Outside fullscreen lock() is a harmless no-op. We deliberately do NOT lock
  // Escape, so it still exits fullscreen.
  const kb = navigator.keyboard;
  if (kb && kb.lock && term.textarea) {
    const KEYS = ["KeyW", "KeyT", "KeyN", "KeyR", "KeyL", "KeyS", "KeyP"];
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
  return term;
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
