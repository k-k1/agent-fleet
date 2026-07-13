// Pane — one slot of the main-area layout. The terminal stays mounted (just
// hidden) while the pane shows another view, so the PTY socket and scrollback
// survive switching kinds. Ported from the old console onto the content union.
import { useEffect, useState } from "react";
import type { CSSProperties, DragEvent as RDragEvent } from "react";
import type { Pane as PaneT } from "../../layout/types.ts";
import { ordClass } from "../../layout/badges.ts";
import { usePaneHover, hoverMatches } from "../../lib/panehover.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { TerminalView } from "../terminal/TerminalView.tsx";
import { MirrorView } from "../mirror/MirrorView.tsx";
import { agentOf } from "../../agents/registry.ts";
import { SourceControlView } from "../scm/SourceControlView.tsx";
import { ChangesView } from "../scm/ChangesView.tsx";
import { CommitDetailView } from "../scm/CommitDetailView.tsx";
import { WorkingDiffView } from "../scm/WorkingDiffView.tsx";
import { FileView } from "../viewer/FileView.tsx";
import { ReaderView } from "../viewer/ReaderView.tsx";
import { DocView } from "../viewer/DocView.tsx";
import { DiffView } from "../viewer/DiffView.tsx";
import type { DiffEdit } from "../viewer/DiffView.tsx";
import { ChatView } from "../chat/ChatView.tsx";
import { useSettings } from "../../lib/settings.ts";
import { IconButton } from "../../ui/Button.tsx";
import { cx } from "../../ui/cx.ts";
import type { Session } from "../../types/session.ts";

// Drag payload MIME — identifies a pane-to-pane drag (vs any other drag).
const DND = "application/x-af-pane";


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

export function Pane({
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
  const isTerm = pane.content.kind === "terminal";
  // Cross-highlight: glow this pane while its rail row / mini-map cell is
  // hovered (and vice-versa). Keyed by pane id or a shared session name.
  const { hover, setHover } = usePaneHover();
  const hovered = hoverMatches(hover, pane.id, pane.session);
  const ordCls = ordinal ? ordClass(ordinal) : "";
  // null when not a drop target; else the pointer's zone: 'center' → swap;
  // 'right'/'down' → tear the dragged pane off into a new split.
  const [zone, setZone] = useState<string | null>(null);

  // Markdown mirror (case-A): a claude session pane can swap its raw terminal for
  // a read-mostly chat view. A stopped session opened as chat starts DETACHED
  // (read-only history, no resume) — 再開して続ける / the ターミナル toggle attaches.
  const chat = pane.content.kind === "terminal" && pane.content.chat;
  const [mirror, setMirror] = useState(chat === true);
  const [attached, setAttached] = useState(!chat || sessionMeta?.alive === true);
  // Re-sync when the pane's session/chat descriptor changes (a new session opened
  // here); local toggles persist within the same session.
  useEffect(() => {
    setMirror(chat === true);
    setAttached(!chat || sessionMeta?.alive === true);
  }, [pane.session, chat]);
  // Track liveness: alive → attach (connecting doesn't resume); stopped → detach
  // to read-only so nothing silently resumes. Runs only on an alive CHANGE, so a
  // user resume (attached=true while alive still false) isn't undone.
  useEffect(() => {
    if (sessionMeta?.alive === true) setAttached(true);
    else if (sessionMeta?.alive === false) setAttached(false);
  }, [sessionMeta?.alive]);
  // The mirror is offered only for agents with the `chat` capability (claude —
  // /messages is claude-only). Guard on loaded sessionMeta.
  const canMirror = isTerm && !!pane.session && !!sessionMeta && agentOf(sessionMeta.kind).caps.chat;
  const showMirror = canMirror && mirror;
  // The ターミナル toggle also resumes a stopped session (attach is explicit).
  const onToggleMirror = (toChat: boolean) => {
    if (toChat) setMirror(true);
    else {
      setMirror(false);
      onResume();
    }
  };

  // Per-pane line-wrap override for text views (null = follow the global setting).
  const setPaneWrap = useLayoutStore((s) => s.setPaneWrap);
  const settings = useSettings();
  const wrapOn = pane.wrap ?? settings.wrap;
  const canWrap =
    pane.content.kind === "file" || pane.content.kind === "diff" || pane.content.kind === "wtdiff";

  // Resume a stopped session EXPLICITLY (the terminal WS is connect-only): POST
  // /start, then attach. An already-alive session just attaches.
  const startSession = useSessionsStore((s) => s.start);
  const onResume = () => {
    void (async () => {
      if (sessionMeta?.alive !== true && pane.session) await startSession(pane.session);
      setAttached(true);
    })();
  };

  const onDragStart = (e: RDragEvent) => {
    e.dataTransfer.setData(DND, pane.id);
    e.dataTransfer.effectAllowed = "move";
  };
  // Outer 30% of the splittable edges is a split zone; the center swaps.
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
      className={cx("pane", active && "active", zone && "droptarget", hovered && "pane-hover", ordCls)}
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
        // matches the rail and mini-map, still draggable to swap.
        <button
          type="button"
          className={cx("pane-grip", ordinal ? "pane-ord " + ordCls : "")}
          title={ordinal ? `ペイン${ordinal} — ドラッグして他のペインと入れ替え` : "ドラッグして入れ替え"}
          draggable
          onDragStart={onDragStart}
        >
          {ordinal ?? <span className="codicon codicon-gripper" aria-hidden="true" />}
        </button>
      )}
      <div className="pane-controls">
        {canWrap && (
          <IconButton
            icon="word-wrap"
            label={wrapOn ? "折り返しを解除" : "行を折り返す"}
            className={wrapOn ? "on" : ""}
            onClick={() => setPaneWrap(pane.id, !wrapOn)}
          />
        )}
        {canClose && (
          <IconButton
            icon="close"
            label="このペインを閉じる（中クリック / Ctrl+クリックで直接閉じる）"
            className="pane-close"
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                onClose(pane.id, true);
              }
            }}
            onClick={(e) => onClose(pane.id, e.ctrlKey || e.metaKey)}
          />
        )}
      </div>

      {/* Drop hint while dragging a pane over this one. */}
      {zone && <div className={"drop-indicator zone-" + zone} />}

      {/* The terminal stays mounted for any terminal-kind pane; other kinds keep
          the pane's xterm warm in the service (not mounted) until their view
          port lands. */}
      {/* The terminal stays MOUNTED (hidden) while the mirror shows, so the PTY
          socket + scrollback survive the toggle. */}
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
          paneId={pane.id}
          session={pane.session!}
          sessionMeta={sessionMeta}
          active={single || active}
          mirror={mirror}
          onToggleMirror={onToggleMirror}
          readOnly={!attached}
          onResume={onResume}
        />
      )}
      {pane.content.kind === "scm" && <SourceControlView repo={pane.content.scmRepo} />}
      {pane.content.kind === "changes" && <ChangesView repo={pane.content.scmRepo} />}
      {pane.content.kind === "commit" && (
        <CommitDetailView repo={pane.content.scmRepo} sha={pane.content.commitSha} wrap={wrapOn} />
      )}
      {pane.content.kind === "wtdiff" && (
        <WorkingDiffView
          repo={pane.content.scmRepo}
          path={pane.content.filePath}
          staged={pane.content.diffStaged}
          wrap={wrapOn}
        />
      )}
      {pane.content.kind === "file" && (
        <FileView
          filePath={pane.content.filePath}
          targetLine={pane.content.targetLine}
          targetColumn={pane.content.targetColumn}
          wrap={pane.wrap}
        />
      )}
      {pane.content.kind === "read" && <ReaderView filePath={pane.content.filePath} />}
      {pane.content.kind === "doc" && <DocView title={pane.content.docTitle} content={pane.content.docContent} />}
      {pane.content.kind === "diff" && (
        <DiffView
          title={pane.content.docTitle}
          tool={pane.content.diffTool}
          edits={pane.content.diffEdits as DiffEdit[]}
          wrap={wrapOn}
        />
      )}
      {pane.content.kind === "chat" && (
        <ChatView
          conversationId={pane.content.conversationId}
          draftAssistantId={pane.content.draftAssistantId}
          paneId={pane.id}
          active={single || active}
        />
      )}
    </div>
  );
}
