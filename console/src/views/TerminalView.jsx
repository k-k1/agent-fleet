import { useEffect, useMemo, useRef } from "react";
import { ensureTerm, fit, focusTerm, attach, detach, reconnect } from "../term.js";
import { rel } from "../api.js";
import TermKeys from "../components/TermKeys.jsx";
import Icon from "../components/Icon.jsx";
import MirrorToggle from "../components/MirrorToggle.jsx";
import ContextBar from "../components/ContextBar.jsx";
import OnboardingCard from "../components/OnboardingCard.jsx";
import { kindIcon, kindLabel, kindShort, kindClass } from "../lib/sessionkind.js";
import { displayName, stateInfo } from "../lib/sessionview.js";
import { useContextUsage } from "../lib/usecontextusage.js";

// Brand artwork shown over an unattached terminal so a freshly split (or initial)
// pane isn't a bare black rectangle. Each empty pane picks one of these at random.
// Resolved against baseURI so they work behind the path-stripping proxy.
// (Live in public/brand, served by the Control Plane.)
const IDLE_ARTWORK = Array.from({ length: 7 }, (_, i) => rel(`brand/idle-${i + 1}.png`));

// TerminalView hosts one pane's xterm instance (keyed by paneId). The container
// stays mounted while the pane shows a terminal (Pane hides it rather than
// unmounting), so the PTY socket and scrollback survive view switches. The PTY
// connection follows the `session` prop declaratively: the pane descriptor is the
// source of truth, and we attach/detach to match it. (Fullscreen is a global
// control in TopBar.)
export default function TerminalView({
  paneId = "p0",
  session = null,
  sessionMeta = null,
  active,
  canMirror = false,
  mirror = false,
  onToggleMirror,
}) {
  const ref = useRef(null);

  // Pick one idle image per mounted pane and keep it stable across re-renders
  // (so it doesn't reshuffle every time the session prop toggles).
  const idleSrc = useMemo(
    () => IDLE_ARTWORK[Math.floor(Math.random() * IDLE_ARTWORK.length)],
    [],
  );

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
      reconnect(paneId); // recover a dropped PTY when this pane becomes active
    }
  }, [active, paneId]);

  // Header mirrors the left-pane Sessions row: kind badge + display name + the
  // session id + a status chip. Falls back to bare text before the session's
  // metadata has arrived (freshly created), or when the pane has no session.
  const st = sessionMeta ? stateInfo(sessionMeta) : null;

  // Current context fill, shown as a strip under the head like the chat view. Poll
  // only while the terminal is the visible view (!mirror) — the chat view runs its
  // own poll when it's up, so at most one is active per pane.
  const ctxUsage = useContextUsage(session, sessionMeta?.kind, !mirror);

  return (
    <div className="termview">
      <header className="view-head">
        {session && sessionMeta ? (
          <span className="pane-session">
            <span className={"kind-tag kind-" + kindClass(sessionMeta.kind)}>
              <Icon name={kindIcon(sessionMeta.kind)} />
              <span className="kt-label kt-full">{kindLabel(sessionMeta.kind)}</span>
              <span className="kt-label kt-short">{kindShort(sessionMeta.kind)}</span>
            </span>
            <span className="session-display">{displayName(sessionMeta)}</span>
            <span className="session-name">{sessionMeta.name}</span>
            <span className={"session-state " + st.cls}>
              <Icon name={st.icon} spin={st.spin} /> {st.text}
            </span>
          </span>
        ) : (
          <span className="view-title">{session ? `session: ${session}` : "セッション未接続"}</span>
        )}
        {canMirror && <MirrorToggle mirror={mirror} onToggle={onToggleMirror} />}
      </header>
      {ctxUsage && <ContextBar {...ctxUsage} />}
      <div className="term-body">
        <div className="terminal" ref={ref} />
        {/* No session: cover the bare terminal with the FileViewer-tinted brand
            placeholder instead of an empty black box. */}
        {!session && (
          <div className="term-empty">
            <img className="term-empty-img" src={idleSrc} alt="Agent Fleet" />
            {/* First-run checklist, only on the active empty pane. Renders null once
                set up / dismissed, leaving just the brand placeholder. */}
            {active && <OnboardingCard />}
          </div>
        )}
      </div>
      <TermKeys paneId={paneId} />
    </div>
  );
}
