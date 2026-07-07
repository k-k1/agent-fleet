// TerminalView — hosts one pane's xterm instance (keyed by paneId), ported from
// the old console onto the terminal service + zustand stores. The container
// stays mounted while the pane shows a terminal (Pane hides it rather than
// unmounting), so the PTY socket and scrollback survive view switches. The PTY
// connection follows the `session` prop declaratively.
//
// Not yet ported (docs/22): MirrorToggle/chat mirror (P5), ContextBar (P6),
// OnboardingCard (P6).
import { useEffect, useMemo, useRef } from "react";
import {
  ensureTerm,
  fit,
  focusTerm,
  attach,
  detach,
  reconnect,
  ensureAttached,
  setTermBackground,
  clearTerm,
} from "../../terminal/service.ts";
import { termBackground } from "../../lib/termcolor.ts";
import { rel } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { kindIcon, kindLabel, kindShort, kindClass } from "../../lib/sessionkind.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { Button } from "../../ui/Button.tsx";
import { Pill } from "../../ui/Pill.tsx";
import type { PillTone } from "../../ui/Pill.tsx";
import { TermKeys } from "./TermKeys.tsx";
import type { Session } from "../../types/session.ts";

// Brand artwork shown over an unattached terminal so a freshly split (or initial)
// pane isn't a bare black rectangle. Resolved against baseURI (path-strip proxy).
const IDLE_ARTWORK = Array.from({ length: 7 }, (_, i) => rel(`brand/idle-${i + 1}.png`));

// stateInfo cls → Pill tone (the old console themed these via CSS state classes).
const STATE_TONE: Record<string, PillTone> = {
  on: "ok",
  working: "accent",
  question: "warn",
  bg: "warn",
  off: "muted",
  "off dead": "danger",
};

interface TerminalViewProps {
  paneId: string;
  session: string | null;
  sessionMeta?: Session | null;
  active?: boolean;
  attached?: boolean;
  onResume?: () => void;
}

export function TerminalView({
  paneId,
  session,
  sessionMeta = null,
  active,
  attached = true,
  onResume,
}: TerminalViewProps) {
  const ref = useRef<HTMLDivElement>(null);
  const running = useWorkspaceStore((s) => s.state) === "running";
  // Session is stopped while shown here → mask the disconnected terminal with an
  // in-place 再開 (resume is explicit; auto-resume is abolished).
  const stopped = !!session && sessionMeta != null && sessionMeta.alive === false;

  // One idle image per mounted pane, stable across re-renders.
  const idleSrc = useMemo(() => IDLE_ARTWORK[Math.floor(Math.random() * IDLE_ARTWORK.length)], []);

  useEffect(() => {
    ensureTerm(paneId, ref.current!);
  }, [paneId]);

  // Tint the terminal background by the session's kind (and SSM host color).
  useEffect(() => {
    setTermBackground(paneId, termBackground(sessionMeta?.kind, sessionMeta?.color));
  }, [paneId, sessionMeta?.kind, sessionMeta?.color]);

  // Sync the WebSocket to the descriptor. Attach only while the pane is attached
  // AND the workspace is running — opening /ws/pty is what the CP treats as
  // intent-to-work (auto-start), so a click while stopped must not boot the WS.
  useEffect(() => {
    if (session && attached && running) attach(paneId, session);
    else {
      detach(paneId);
      // A session-less pane is an empty terminal: wipe scrollback left over from
      // a session this reused xterm previously showed. A stopped session keeps
      // its `session` set, so its read-only history isn't cleared here.
      if (!session) clearTerm(paneId);
    }
  }, [paneId, session, attached, running]);

  // Guard against accidentally closing/reloading the tab while a session is attached.
  useEffect(() => {
    if (!session) return;
    const handler = (e: BeforeUnloadEvent) => {
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
      // Recover a dropped PTY when this pane becomes active — but never silently
      // resume a stopped session or auto-start a stopped workspace.
      if (attached && running) reconnect(paneId);
    }
  }, [active, paneId, attached, running]);

  // Reveal fit: when a hidden container becomes visible its size was 0×0 — defer
  // past layout (rAF) so the grid measures the real size and the TUI redraws.
  useEffect(() => {
    if (!session || !attached || !running) return;
    const raf = requestAnimationFrame(() => ensureAttached(paneId, session));
    return () => cancelAnimationFrame(raf);
  }, [paneId, session, attached, running]);

  const st = sessionMeta ? stateInfo(sessionMeta) : null;

  return (
    <div className="termview">
      <header className="pane-head">
        {session && sessionMeta ? (
          <span className="pane-session" title={"ID: " + sessionMeta.name}>
            <span className={"kind-tag kind-" + kindClass(sessionMeta.kind)}>
              <span className={`codicon codicon-${kindIcon(sessionMeta.kind)}`} aria-hidden="true" />
              <span className="kt-full">{kindLabel(sessionMeta.kind)}</span>
              <span className="kt-short">{kindShort(sessionMeta.kind)}</span>
            </span>
            <span className="pane-session-name">{displayName(sessionMeta)}</span>
            <Pill tone={STATE_TONE[st!.cls] || "muted"} icon={st!.icon}>
              {st!.text}
            </Pill>
          </span>
        ) : (
          <span className="pane-head-title">{session ? "セッション" : "セッション未接続"}</span>
        )}
      </header>
      <div className="term-body">
        <div className="terminal" ref={ref} />
        {!session && (
          <div className="term-empty">
            <img className="term-empty-img" src={idleSrc} alt="Agent Fleet" />
          </div>
        )}
        {stopped && (
          <div className="term-mask">
            {attached && running ? (
              <span className="term-mask-msg">
                <span className="codicon codicon-loading codicon-spin" aria-hidden="true" /> 再開中…
              </span>
            ) : (
              <Button
                variant="primary"
                icon="play"
                disabled={!running}
                title={running ? "このセッションを再開" : "ワークスペース停止中"}
                onClick={() => onResume?.()}
              >
                再開
              </Button>
            )}
          </div>
        )}
      </div>
      <TermKeys paneId={paneId} />
    </div>
  );
}
