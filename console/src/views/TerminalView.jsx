import { useEffect, useRef, useState } from "react";
import { ensureTerm, fit, focusTerm, onSession } from "../term.js";
import TermKeys from "../components/TermKeys.jsx";

// TerminalView hosts the persistent xterm instance. The container stays mounted
// across mode switches (App hides it rather than unmounting), so the PTY socket
// and scrollback survive. On re-show we refit so the geometry is correct.
// (Fullscreen is a global control in the TopBar.)
export default function TerminalView({ active }) {
  const ref = useRef(null);
  const [session, setSession] = useState(null);

  useEffect(() => {
    ensureTerm(ref.current);
    return onSession(setSession);
  }, []);

  // While a session is attached, guard against accidentally closing/reloading the
  // tab (e.g. Ctrl+W on Firefox, which can't be captured) — the browser shows its
  // "leave page?" confirmation. Removed as soon as no session is attached.
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
      fit();
      focusTerm();
    }
  }, [active]);

  return (
    <div className="termview">
      <header className="view-head">
        <span className="view-title">{session ? `session: ${session}` : "セッション未接続"}</span>
        <span className="spacer" />
      </header>
      <div className="terminal" ref={ref} />
      <TermKeys />
    </div>
  );
}
