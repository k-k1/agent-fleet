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
  hideTerm,
  revealTerm,
} from "../../terminal/service.ts";
import { termBackground } from "../../lib/termcolor.ts";
import { useT } from "../../lib/i18n/index.ts";
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
const IDLE_ARTWORK = Array.from({ length: 7 }, (_, i) => rel(`brand/idle-${i + 1}.webp`));

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
  const tr = useT();
  const ref = useRef<HTMLDivElement>(null);
  const running = useWorkspaceStore((s) => s.state) === "running";
  // Session is stopped while shown here → mask the disconnected terminal with an
  // in-place 再開 (resume is explicit; auto-resume is abolished).
  const stopped = !!session && sessionMeta != null && sessionMeta.alive === false;
  // Plain shells expect command input, so entering one with a Japanese IME still
  // active is almost always accidental. The class applies `ime-mode: disabled`
  // to xterm's helper textarea; agent chat terminals keep normal IME behaviour.
  const disableIme = sessionMeta?.kind === "shell" || sessionMeta?.kind === "ssm";

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
    if (session && running && (attached || stopped)) {
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
  }, [paneId, session, attached, running, stopped]);

  // Guard against accidentally closing/reloading the tab while a session is attached.
  useEffect(() => {
    if (!session) return;
    const handler = (e: BeforeUnloadEvent) => {
      // A version-update reload (reloadForUpdate) is intentional — don't block it.
      if ((window as unknown as { __afUpdating?: boolean }).__afUpdating) return;
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

  // Hide/reveal lifecycle for the mirror toggle. A hidden (display:none) WebGL
  // canvas is exactly what browsers reclaim under GPU pressure, and a reclaim
  // that never fires webglcontextlost — or whose restore never comes while the
  // canvas isn't visible — leaves a renderer that looks alive but can never
  // paint again: the black-until-reload pane. So a reveal must never bet on the
  // canvas still holding pixels. Hiding drops the pane's WebGL context
  // (hideTerm); revealing rebuilds it and repaints every row from the buffer
  // (revealTerm), deferred past layout (rAF) so the grid measures the real size.
  //
  // The socket side is guarded separately: re-verify on the SAME retry ladder as
  // the initial attach — the attach effect above does NOT re-run on a mirror
  // toggle (its deps session/attached/running don't change), so a reveal whose
  // attach raced (the WS never opened, dropped, or opened but never drew) would
  // otherwise sit on a dead socket. revealTerm runs even detached/stopped: a
  // read-only scrollback must repaint too.
  const shown = !(canMirror && mirror);
  useEffect(() => {
    if (!shown) {
      hideTerm(paneId);
      return;
    }
    const connect = !!session && attached && running;
    const raf = requestAnimationFrame(() => {
      if (connect) ensureAttached(paneId, session!);
      revealTerm(paneId);
    });
    const timers = connect
      ? [1500, 4000, 9000].map((ms) => setTimeout(() => ensureAttached(paneId, session!), ms))
      : [];
    return () => {
      cancelAnimationFrame(raf);
      timers.forEach(clearTimeout);
    };
  }, [shown, paneId, session, attached, running]);

  const st = sessionMeta ? stateInfo(sessionMeta) : null;

  // Context fill straight off the session (the Agent computes it) — claude only.
  /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
  const ctxUsage: any = sessionMeta?.context || null;

  return (
    <div className="termview">
      <header className="pane-head">
        {session && sessionMeta ? (
          // The display name ellipsizes in a narrow pane — the hover tip carries it in full.
          <span className="pane-session" title={displayName(sessionMeta) + "\nID: " + sessionMeta.name}>
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
          <span className="pane-head-title">{session ? tr("onb.session") : tr("onb.session_disconnected")}</span>
        )}
        {canMirror && <MirrorToggle mirror={mirror} onToggle={onToggleMirror} running={running} />}
      </header>
      {ctxUsage && <ContextBar {...(ctxUsage as { read: number; create: number; fresh: number; model?: string; window?: number })} />}
      <div className="term-body">
        <div className={"terminal" + (disableIme ? " terminal-ime-disabled" : "")} ref={ref} />
        {!session && (
          <div className="term-empty">
            <img className="term-empty-img" src={idleSrc} alt="Agent Fleet" />
            {/* First-run checklist, only on the active empty pane. Renders null once
                set up / dismissed, leaving just the brand placeholder. */}
            {active && <OnboardingCard />}
          </div>
        )}
        {stopped && (
          <div className="term-history-actions">
            {attached && running ? (
              <span className="term-mask-msg">
                <span className="codicon codicon-loading codicon-spin" aria-hidden="true" /> {tr("onb.resuming")}
              </span>
            ) : (
              <Button
                variant="primary"
                icon="play"
                disabled={!running}
                title={running ? tr("onb.resume_this_session") : tr("onb.ws_stopped")}
                onClick={() => onResume?.()}
              >
                {tr("onb.resume")}
              </Button>
            )}
          </div>
        )}
      </div>
      <TermKeys paneId={paneId} />
    </div>
  );
}
