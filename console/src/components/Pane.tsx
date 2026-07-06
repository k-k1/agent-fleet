import { useEffect, useState } from "react";
import TerminalView from "../views/TerminalView.jsx";
import SourceControlView from "../views/SourceControlView.jsx";
import ChangesView from "../views/ChangesView.jsx";
import CommitDetailView from "../views/CommitDetailView.jsx";
import WorkingDiffView from "../views/WorkingDiffView.jsx";
import FileView from "../views/FileView.jsx";
import MirrorView from "../views/MirrorView.jsx";
import DocView from "../views/DocView.jsx";
import DiffView from "../views/DiffView.jsx";
import ChatView from "../views/ChatView.jsx";
import Icon from "./Icon.jsx";
import { useApp } from "../state.jsx";
import { api } from "../api.js";
import { useSettings } from "../lib/settings.js";
import { ordClass } from "../lib/panebadge.js";
import { usePaneHover, hoverMatches } from "../lib/panehover.jsx";
import { agentOf } from "../agents/registry.ts";
import type { CSSProperties, DragEvent as RDragEvent } from "react";
import type { Pane as PaneT } from "../types/layout.ts";
import type { Session } from "../types/session.ts";

interface PaneProps {
  pane: PaneT;
  style?: CSSProperties;
  active?: boolean;
  single?: boolean;
  canSplitRight?: boolean;
  canSplitDown?: boolean;
  canClose?: boolean;
  canDrag?: boolean;
  onActivate: (id: string) => void;
  onClose: (id: string, remove?: boolean) => void;
  onSwap: (aId: string, bId: string) => void;
  onDropSplit: (srcId: string, refId: string, dir: "right" | "down") => void;
  sessionMeta?: Session | null;
  ordinal?: number | null;
}

// Drag payload MIME — identifies a pane-to-pane swap drag (vs any other drag).
const DND = "application/x-af-pane";

// Pane renders one slot of the main-area layout. Like the original single-pane app,
// the terminal stays mounted (just hidden) while the pane shows a file/scm view, so
// the PTY socket and scrollback survive switching kinds. The file/scm views overlay
// on top when active. A top-right control cluster holds a drag grip (drag onto
// another pane to swap), split-right, split-down, and close; a mousedown anywhere
// makes this the active pane (clicks then open here).
export default function Pane({
  pane,
  style,
  active,
  single,
  canSplitRight,
  canSplitDown,
  canClose,
  canDrag,
  onActivate,
  onClose,
  onSwap,
  onDropSplit,
  sessionMeta,
  ordinal,
}: PaneProps) {
  const isTerm = pane.kind === "terminal";
  // Cross-highlight: glow this pane while its Sessions-list row or mini-map cell is
  // hovered (and vice-versa — entering the pane lights them). Keyed by pane id or a
  // shared session name. See lib/panehover.jsx.
  const { hover, setHover } = usePaneHover();
  const hovered = hoverMatches(hover, pane.id, pane.session);
  const ordCls = ordinal ? ordClass(ordinal) : "";
  // null when not a drop target; otherwise the zone the pointer is in:
  //   'center' → swap with the dragged pane; 'right'/'down' → tear the dragged
  //   pane off into a new split (new right column / downward split of this column).
  const [zone, setZone] = useState<string | null>(null);

  // Markdown mirror toggle (case-A): a claude session pane can swap its raw terminal
  // for a read-mostly Markdown view of the conversation. Only offered for claude (the
  // /messages endpoint is claude-only). `attached` = the TerminalView is mounted (PTY
  // live). A stopped session opened as chat (pane.chat) starts DETACHED: chat history
  // is shown read-only WITHOUT resuming; pressing 再開して続ける (or the ターミナル
  // toggle) attaches, which relaunches the session. The terminal stays mounted once
  // attached so the PTY survives view switches.
  const [mirror, setMirror] = useState(pane.chat === true);
  // Detached (read-only chat) is only meaningful for a STOPPED session — history you can
  // read without silently resuming it. A session opened as chat that is ALREADY alive
  // must start attached, else MirrorView shows 再開して続ける over a live, input-ready
  // session (the reported bug). Attaching a live session just connects to its running
  // PTY; it doesn't resume anything.
  const [attached, setAttached] = useState(pane.chat !== true || sessionMeta?.alive === true);
  // Re-sync when the pane's session/chat descriptor changes (a new session opened in
  // this pane). Local toggles (setMirror/setAttached) don't touch these deps, so a
  // user's resume/switch within the same session persists.
  useEffect(() => {
    setMirror(pane.chat === true);
    setAttached(pane.chat !== true || sessionMeta?.alive === true);
  }, [pane.session, pane.chat]);
  // Track the session's liveness: a live session is interactive, so attach (connecting to
  // an already-running session doesn't resume it). A stopped one detaches to read-only
  // history so nothing silently reconnects/resumes it (auto-resume is abolished); resume
  // stays explicit via 再開して続ける. Downgrading-only used to leave a session opened as
  // chat while alive stuck read-only. A user resume sets attached=true while alive is
  // still false; this effect only runs on an alive CHANGE, so it won't undo that.
  useEffect(() => {
    if (sessionMeta?.alive === true) setAttached(true);
    else if (sessionMeta?.alive === false) setAttached(false);
  }, [sessionMeta?.alive]);
  // The chat mirror is offered only for agents whose descriptor declares the `chat`
  // capability (today: claude, the /messages endpoint is claude-only). Guard on a
  // loaded sessionMeta so an unknown kind doesn't default-open the mirror.
  const canMirror = isTerm && !!pane.session && !!sessionMeta && agentOf(sessionMeta.kind).caps.chat;
  const showMirror = canMirror && mirror;
  // Resume a stopped session EXPLICITLY: the terminal WS is now connect-only (it no
  // longer auto-starts a session on attach), so a stopped session must be relaunched via
  // POST /start before attaching. An already-alive session just attaches (no start).
  const resumeIfStopped = async () => {
    if (sessionMeta?.alive !== true && pane.session) {
      try {
        await api(`api/sessions/${encodeURIComponent(pane.session)}/start`, { method: "POST" });
      } catch {
        /* attach still tries; a failure surfaces as [disconnected] */
      }
    }
    setAttached(true);
  };
  // ターミナル toggle shows the terminal AND resumes a stopped session. チャット toggle
  // just shows the chat overlay (read-only for a stopped session).
  const onToggleMirror = (toChat: boolean) => {
    if (toChat) setMirror(true);
    else {
      setMirror(false);
      void resumeIfStopped();
    }
  };
  // 再開して続ける: resume (start if needed) in the background while keeping the chat
  // open; the composer enables once the session is live (MirrorView watches alive).
  const onResume = () => void resumeIfStopped();

  // Per-pane line-wrap: a file pane can toggle wrapping independently of the global
  // setting (null = inherit the setting). The toggle sits in the pane-control cluster.
  const { setPaneWrap } = useApp();
  const settings = useSettings();
  const wrapOn = pane.wrap ?? settings.wrap;
  const canWrap = pane.kind === "file" || pane.kind === "diff" || pane.kind === "wtdiff";

  const onDragStart = (e: RDragEvent) => {
    e.dataTransfer.setData(DND, pane.id);
    e.dataTransfer.effectAllowed = "move";
  };
  // Outer 30% of the splittable edges is a split zone; the center swaps. A split
  // edge is only offered when this pane can grow that way (else it stays center).
  const zoneFor = (e: RDragEvent): "center" | "down" | "right" => {
    const r = e.currentTarget.getBoundingClientRect();
    const rd = canSplitRight ? (e.clientX - r.left) / r.width - 0.7 : -1;
    const dd = canSplitDown ? (e.clientY - r.top) / r.height - 0.7 : -1;
    if (rd < 0 && dd < 0) return "center";
    return dd > rd ? "down" : "right";
  };
  const onDragOver = (e: RDragEvent) => {
    if (!canDrag || !e.dataTransfer.types.includes(DND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const z = zoneFor(e);
    setZone((prev) => (prev === z ? prev : z));
  };
  const onDragLeave = (e: RDragEvent) => {
    // Ignore bubbling from descendants; only clear when leaving the pane itself.
    if (e.currentTarget.contains(e.relatedTarget as Node)) return;
    setZone(null);
  };
  const onDrop = (e: RDragEvent) => {
    if (!e.dataTransfer.types.includes(DND)) return;
    e.preventDefault();
    const z = zone;
    setZone(null);
    const src = e.dataTransfer.getData(DND);
    if (!src) return;
    if (z === "right" || z === "down") onDropSplit(src, pane.id, z);
    else onSwap(src, pane.id);
  };

  return (
    <div
      className={
        "pane" +
        (active ? " active" : "") +
        (zone ? " droptarget" : "") +
        (hovered ? " pane-hover" : "") +
        (ordCls ? " " + ordCls : "")
      }
      style={style}
      onMouseDownCapture={() => onActivate(pane.id)}
      onMouseEnter={() => setHover({ session: pane.session || null, paneId: pane.id })}
      onMouseLeave={() => setHover(null)}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {canDrag && (
        // The drag grip doubles as the pane's ordinal chip: a colored number that
        // matches the Sessions row and mini-map, and is still draggable to swap.
        <button
          type="button"
          className={"pane-grip" + (ordinal ? " pane-ord " + ordCls : " ghost pane-btn")}
          title={
            ordinal
              ? `ペイン${ordinal} — ドラッグして他のペインと入れ替え`
              : "ドラッグして他のペインと入れ替え"
          }
          draggable
          onDragStart={onDragStart}
        >
          {ordinal ? <span className="pane-ord-num">{ordinal}</span> : <Icon name="gripper" />}
        </button>
      )}
      <div className="pane-controls">
        {canWrap && (
          <button
            type="button"
            className={"ghost pane-btn" + (wrapOn ? " active" : "")}
            title={wrapOn ? "折り返しを解除" : "行を折り返す"}
            onClick={() => setPaneWrap(pane.id, !wrapOn)}
          >
            <Icon name="word-wrap" />
          </button>
        )}
        {/* Split (右に分割 / 上下に分割) moved to the WS bar (acts on the active pane),
            freeing space in this cramped per-pane header. Drag-to-split still uses
            canSplitRight/canSplitDown below. Per-pane × (close) stays — it's inherently
            per-pane (the WS bar has the global 全て閉じる). */}
        {canClose && (
          <button
            type="button"
            className="ghost pane-btn pane-close"
            title="このペインを閉じる（中クリック / Ctrl+クリックで空にせず直接閉じる）"
            // Middle-click also closes outright; suppress the mousedown default so the
            // browser doesn't start autoscroll.
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                onClose(pane.id, true);
              }
            }}
            // Plain click: blank-then-remove. Ctrl/⌘+click: remove the pane outright.
            onClick={(e) => onClose(pane.id, e.ctrlKey || e.metaKey)}
          >
            <Icon name="close" />
          </button>
        )}
      </div>

      {/* Drop hint while dragging a pane over this one: a full-pane ring for a
          swap (center), or a half-pane box on the edge where the new split lands. */}
      {zone && <div className={"drop-indicator zone-" + zone} />}

      {/* Terminal stays mounted for any terminal-kind pane so its xterm/scrollback are
          stable across view switches; `hidden` handles visibility. Whether the PTY is
          live (and thus whether a stopped session resumes) is gated by `attached`, not
          by mounting — a read-only/stopped pane mounts the terminal but doesn't attach,
          so there's no socket and no resume. */}
      {isTerm && (
        <div className="view" hidden={showMirror}>
          <TerminalView
            paneId={pane.id}
            session={pane.session}
            sessionMeta={sessionMeta}
            active={(single || active) && isTerm && !showMirror}
            attached={attached}
            canMirror={canMirror}
            mirror={mirror}
            onToggleMirror={onToggleMirror}
            onResume={onResume}
          />
        </div>
      )}
      {showMirror && (
        <MirrorView
          session={pane.session!}
          sessionMeta={sessionMeta}
          active={single || active}
          mirror={mirror}
          onToggleMirror={onToggleMirror}
          readOnly={!attached}
          onResume={onResume}
        />
      )}
      {pane.kind === "scm" && <SourceControlView repo={pane.scmRepo ?? undefined} wrap={wrapOn} />}
      {pane.kind === "changes" && <ChangesView repo={pane.scmRepo ?? undefined} wrap={wrapOn} />}
      {pane.kind === "commit" && (
        <CommitDetailView repo={pane.scmRepo ?? undefined} sha={pane.commitSha ?? undefined} wrap={wrapOn} />
      )}
      {pane.kind === "wtdiff" && (
        <WorkingDiffView repo={pane.scmRepo ?? undefined} path={pane.filePath} staged={pane.diffStaged} wrap={wrapOn} />
      )}
      {pane.kind === "chat" && (
        <ChatView
          conversationId={pane.conversationId}
          draftAssistantId={pane.draftAssistantId}
          paneId={pane.id}
          active={single || active}
        />
      )}
      {pane.kind === "file" && <FileView filePath={pane.filePath} wrap={wrapOn} />}
      {pane.kind === "doc" && <DocView title={pane.docTitle ?? undefined} content={pane.docContent ?? undefined} />}
      {pane.kind === "diff" && (
        <DiffView
          title={pane.docTitle ?? undefined}
          tool={pane.diffTool ?? undefined}
          edits={pane.diffEdits as any}
          wrap={wrapOn}
        />
      )}
    </div>
  );
}
