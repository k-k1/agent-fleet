// TerminalView — hosts one pane's xterm instance (keyed by paneId), ported from
// the old console onto the terminal service + zustand stores. The container
// stays mounted while the pane shows a terminal (Pane hides it rather than
// unmounting), so the PTY socket and scrollback survive view switches. The PTY
// connection follows the `session` prop declaratively.
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
import { Icon } from "../../ui/Icon.tsx";
import { TermKeys } from "./TermKeys.tsx";
import { MirrorToggle } from "../mirror/MirrorToggle.tsx";
import { OnboardingCard } from "./OnboardingCard.tsx";
import { ContextBar } from "../mirror/ContextBar.tsx";
import type { Session } from "../../types/session.ts";

// Brand artwork shown over an unattached terminal so a freshly split (or initial)
// pane isn't a bare black rectangle. Resolved against baseURI (path-strip proxy).
const IDLE_ARTWORK = Array.from({ length: 7 }, (_, i) => rel(`brand/idle-${i + 1}.png`));

interface TerminalViewProps {
  paneId: string;
  session: string | null;
  sessionMeta?: Session | null;
  active?: boolean;
  attached?: boolean;
  canMirror?: boolean;
  mirror?: boolean;
  onToggleMirror?: (toChat: boolean) => void;
  onResume?: () => void;
}

export function TerminalView({
  paneId,
  session,
  sessionMeta = null,
  active,
  attached = true,
  canMirror = false,
  mirror = false,
  onToggleMirror,
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
    if (session && attached && running) {
      attach(paneId, session);
      // A session opened right after creation (repo 起動 / worktree launch) can
      // race its own bring-up: the first PTY connect may fail or die while the
      // agent is still starting, and the focus-reconnect path only fires on a
      // REFOCUS — leaving a silent black pane. Re-verify the socket a few times;
      // ensureAttached no-ops (just refits) when the connection is live.
      const timers = [1500, 4000, 9000].map((ms) =>
        setTimeout(() => ensureAttached(paneId, session), ms),
      );
      return () => timers.forEach(clearTimeout);
    }
    detach(paneId);
    // A session-less pane is an empty terminal: wipe scrollback left over from
    // a session this reused xterm previously showed. A stopped session keeps
    // its `session` set, so its read-only history isn't cleared here.
    if (!session) clearTerm(paneId);
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

  // Reveal fit: when a hidden container becomes visible (toggled back from the
  // chat mirror / a split un-hidden) its size was 0×0 — defer past layout (rAF)
  // so the grid measures the real size and the TUI redraws.
  const shown = !(canMirror && mirror);
  useEffect(() => {
    if (!shown || !session || !attached || !running) return;
    const raf = requestAnimationFrame(() => ensureAttached(paneId, session));
    return () => cancelAnimationFrame(raf);
  }, [shown, paneId, session, attached, running]);

  const st = sessionMeta ? stateInfo(sessionMeta) : null;

  // Context fill straight off the session (the Agent computes it) — claude only.
  /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
  const ctxUsage: any = sessionMeta?.context || null;

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
            <span className={"session-state " + st!.cls}>
              <Icon name={st!.icon} spin={st!.spin} /> {st!.text}
            </span>
          </span>
        ) : (
          <span className="pane-head-title">{session ? "セッション" : "セッション未接続"}</span>
        )}
        {canMirror && <MirrorToggle mirror={mirror} onToggle={onToggleMirror} running={running} />}
      </header>
      {ctxUsage && <ContextBar {...(ctxUsage as { read: number; create: number; fresh: number; model?: string; window?: number })} />}
      <div className="term-body">
        <div className="terminal" ref={ref} />
        {!session && (
          <div className="term-empty">
            <img className="term-empty-img" src={idleSrc} alt="Agent Fleet" />
            {/* First-run checklist, only on the active empty pane. Renders null once
                set up / dismissed, leaving just the brand placeholder. */}
            {active && <OnboardingCard />}
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
