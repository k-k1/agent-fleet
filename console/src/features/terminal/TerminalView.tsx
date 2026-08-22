// TerminalView — hosts one pane's xterm instance (keyed by paneId), ported from
// the old console onto the terminal service + zustand stores. The container
// stays mounted while the pane shows a terminal (Pane hides it rather than
// unmounting), so the PTY socket and scrollback survive view switches. The PTY
// connection follows the `session` prop declaratively.
import { useEffect, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import {
  ensureTerm,
  repaint,
  focusTerm,
  attach,
  detach,
  reconnect,
  ensureAttached,
  setTermBackground,
  clearTerm,
  hideTerm,
  revealTerm,
  sessionOf,
} from "../../terminal/service.ts";
import { termBackground } from "../../lib/termcolor.ts";
import { useT } from "../../lib/i18n/index.ts";
import { rel } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { Button } from "../../ui/Button.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { PaneSessionChip } from "../panes/PaneSessionChip.tsx";
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
  /** A resume request is in flight (Pane owns it). Drives the spinner — reading that
   * off `attached` latched the overlay on forever when a resume failed. */
  resuming?: boolean;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
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
  resuming = false,
  headerActions,
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
      // Only open a FRESH socket (which resets the terminal + steals focus) when
      // this pane isn't already on this session. A redundant effect re-run — e.g.
      // the 4s sessions poll re-publishing the list re-renders this pane — must not
      // reset() the grid (wiping the visible history) or yank focus every tick. When
      // already on this session, ensureAttached recovers a dead socket but no-ops
      // (just refits) on a live one, so it's non-disruptive.
      // `attached` = we intend a LIVE PTY (a running session, or an explicit 再開).
      // `stopped && !attached` = viewing a STOPPED session's READ-ONLY history: the CP
      // serves a finite scrollback replay that closes cleanly (code 1000), so the socket
      // ends in CLOSED. The live-recovery machinery below must NOT touch that: ensureAttached
      // treats a CLOSED socket as "not live" and re-attaches, and attach() resets the grid and
      // replays the whole history — so re-verifying a stopped session blanks-and-repaints the
      // pane on every retry (the "切断された shell/ssm が4秒ごとにちらつく" bug), and re-opening
      // /ws/pty is "intent-to-work" so it can even silently auto-start the stopped session.
      // Load the history ONCE, then leave it alone. Only a genuinely live session gets the
      // re-verify + retry ladder (which exists to repaint a live pane that raced its bring-up).
      const wantLive = attached;
      if (sessionOf(paneId) !== session) attach(paneId, session);
      else if (wantLive) ensureAttached(paneId, session);
      if (!wantLive) return; // stopped read-only history: no re-verify, no retry ladder
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
      // repaint (forceFit), not fit: becoming the active pane must reliably restore a black
      // pane, and plain fit() no-ops when the grid shape is unchanged. On touch, focusTerm is
      // a no-op (won't summon the keyboard just to read), so the focus-handler repaint never
      // fires — activating the pane is the only recovery hook, so it must repaint directly.
      repaint(paneId);
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
    // One frame is not enough here: xterm resumes its renderer through an
    // IntersectionObserver after the display:none ancestor is revealed. A refresh
    // in the first frame can therefore still be discarded as "not visible". Wait
    // through the following paint so both layout and xterm's visibility state have
    // settled before rebuilding the renderer and repainting its buffer.
    let revealRaf = 0;
    const layoutRaf = requestAnimationFrame(() => {
      revealRaf = requestAnimationFrame(() => {
        if (connect) ensureAttached(paneId, session!);
        revealTerm(paneId);
      });
    });
    const timers = connect
      ? [1500, 4000, 9000].map((ms) =>
          setTimeout(() => {
            ensureAttached(paneId, session!);
            // The socket retry used to leave an already-connected but unpainted
            // terminal black forever. Retry the renderer side as well; once the
            // pane is visible this is an inexpensive full-buffer repaint.
            revealTerm(paneId);
          }, ms),
        )
      : [];
    return () => {
      cancelAnimationFrame(layoutRaf);
      cancelAnimationFrame(revealRaf);
      timers.forEach(clearTimeout);
    };
  }, [shown, paneId, session, attached, running]);

  const st = sessionMeta ? stateInfo(sessionMeta) : null;

  // Context fill straight off the session (the Agent computes it) — claude only.
  const ctxUsage = sessionMeta?.context || null;

  return (
    <div className="termview">
      <ViewHead
        className="view-head-term"
        actions={
          <>
            {canMirror && <MirrorToggle mirror={mirror} onToggle={onToggleMirror} running={running} />}
            {/* 末尾＝右端。ミラー側（MirrorView）と同じ並びにして、チャット/ターミナルを
                切り替えてもセル操作ボタンが同じ位置に留まるようにする。 */}
            {headerActions}
          </>
        }
      >
        {session && sessionMeta ? (
          <PaneSessionChip session={sessionMeta} state={st} />
        ) : (
          <span className="pane-head-title">{session ? tr("onb.session") : tr("onb.session_disconnected")}</span>
        )}
      </ViewHead>
      {ctxUsage && <ContextBar {...ctxUsage} />}
      <div className="term-body">
        <div className={"terminal" + (disableIme ? " terminal-ime-disabled terminal-shellssm" : "")} ref={ref} />
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
            {resuming ? (
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
