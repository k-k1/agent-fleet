// Pane — one slot of the main-area layout. The terminal stays mounted (just
// hidden) while the pane shows another view, so the PTY socket and scrollback
// survive switching kinds. Ported from the old console onto the content union;
// views beyond the terminal render as placeholders until their phase lands
// (P3 SCM, P4 viewers, P5 chat/mirror — docs/22).
import { useEffect, useState } from "react";
import type { CSSProperties, DragEvent as RDragEvent } from "react";
import type { Pane as PaneT, PaneContent } from "../../layout/types.ts";
import { ordClass } from "../../layout/badges.ts";
import { usePaneHover, hoverMatches } from "../../lib/panehover.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { TerminalView } from "../terminal/TerminalView.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { cx } from "../../ui/cx.ts";
import type { Session } from "../../types/session.ts";

// Drag payload MIME — identifies a pane-to-pane drag (vs any other drag).
const DND = "application/x-af-pane";

// Placeholder copy for views whose port hasn't landed yet.
const PENDING: Partial<Record<PaneContent["kind"], { icon: string; label: string; phase: string }>> = {
  scm: { icon: "source-control", label: "ソース管理", phase: "P3" },
  changes: { icon: "diff", label: "変更", phase: "P3" },
  commit: { icon: "git-commit", label: "コミット詳細", phase: "P3" },
  wtdiff: { icon: "diff", label: "ファイル差分", phase: "P3" },
  file: { icon: "file", label: "ファイルビュアー", phase: "P4" },
  doc: { icon: "book", label: "ドキュメント", phase: "P4" },
  diff: { icon: "diff", label: "編集差分", phase: "P4" },
  chat: { icon: "comment-discussion", label: "アシスタントチャット", phase: "P5" },
};

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

  // Whether the PTY is live. A stopped session opened as chat starts DETACHED
  // (read-only history, no resume); the mirror itself is P5, but the alive-edge
  // tracking below is the attach gate the terminal needs today: a live session
  // is interactive (connecting doesn't resume it); a stopped one detaches so
  // nothing silently resumes it — resume stays explicit (再開).
  const chat = pane.content.kind === "terminal" && pane.content.chat;
  const [attached, setAttached] = useState(!chat || sessionMeta?.alive === true);
  useEffect(() => {
    setAttached(!chat || sessionMeta?.alive === true);
  }, [pane.session, chat]);
  useEffect(() => {
    if (sessionMeta?.alive === true) setAttached(true);
    else if (sessionMeta?.alive === false) setAttached(false);
  }, [sessionMeta?.alive]);

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

  const pending = PENDING[pane.content.kind];

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
      {isTerm && (
        <TerminalView
          paneId={pane.id}
          session={pane.session}
          sessionMeta={sessionMeta}
          active={(single || active) && isTerm}
          attached={attached}
          onResume={onResume}
        />
      )}
      {pending && (
        <div className="pane-pending">
          <EmptyState
            icon={pending.icon}
            title={pending.label}
            hint={`このビューは ${pending.phase} で移植されます（旧コンソールでは利用できます）。`}
          />
        </div>
      )}
    </div>
  );
}
