import { useEffect, useRef } from "react";
import { ensureTerm, fit, focusTerm, attach, detach } from "../term.js";
import TermKeys from "../components/TermKeys.jsx";

// TerminalView hosts one pane's xterm instance (keyed by paneId). The container
// stays mounted while the pane shows a terminal (Pane hides it rather than
// unmounting), so the PTY socket and scrollback survive view switches. The PTY
// connection follows the `session` prop declaratively: the pane descriptor is the
// source of truth, and we attach/detach to match it. (Fullscreen is a global
// control in TopBar.)
export default function TerminalView({ paneId = "p0", session = null, active }) {
  const ref = useRef(null);

  useEffect(() => {
    ensureTerm(paneId, ref.current);
  }, [paneId]);

  // Sync the WebSocket to the descriptor. Attaching resets + reconnects, so this
  // only fires when the session value actually changes (React skips re-render on an
  // unchanged prop), preserving scrollback while the same session stays open.
  useEffect(() => {
    if (session) attach(paneId, session);
    else detach(paneId);
  }, [paneId, session]);

  // While a session is attached, guard against accidentally closing/reloading the
  // tab (e.g. Ctrl+W on Firefox, which can't be captured) — the browser shows its
  // "leave page?" confirmation. Any pane with a live session arms it.
  useEffect(() => {
    if (!session) return;
    const handler = (e) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [session]);

  useEffect(() => {
    if (active) {
      fit(paneId);
      focusTerm(paneId);
    }
  }, [active, paneId]);

  return (
    <div className="termview">
      <header className="view-head">
        <span className="view-title">{session ? `session: ${session}` : "セッション未接続"}</span>
        <span className="spacer" />
      </header>
      <div className="terminal" ref={ref} />
      <TermKeys paneId={paneId} />
    </div>
  );
}
