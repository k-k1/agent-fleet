import { useState } from "react";
import TerminalView from "../views/TerminalView.jsx";
import SourceControlView from "../views/SourceControlView.jsx";
import FileView from "../views/FileView.jsx";
import MirrorView from "../views/MirrorView.jsx";
import DocView from "../views/DocView.jsx";
import DiffView from "../views/DiffView.jsx";
import Icon from "./Icon.jsx";
import { useApp } from "../state.jsx";
import { useSettings } from "../lib/settings.js";
import { ordClass } from "../lib/panebadge.js";
import { usePaneHover, hoverMatches } from "../lib/panehover.jsx";

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
  onSplitRight,
  onSplitDown,
  onClose,
  onSwap,
  onDropSplit,
  sessionMeta,
  ordinal,
}) {
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
  const [zone, setZone] = useState(null);

  // Markdown mirror toggle (case-A): a claude session pane can swap its raw terminal
  // for a read-mostly Markdown view of the conversation, driven by the same Agent
  // /output + /input the MCP tools use. Only offered for claude (the /output endpoint
  // is claude-only). The terminal stays mounted underneath so the PTY survives.
  const [mirror, setMirror] = useState(false);
  const canMirror = isTerm && !!pane.session && sessionMeta?.kind === "claude";
  const showMirror = canMirror && mirror;

  // Per-pane line-wrap: a file pane can toggle wrapping independently of the global
  // setting (null = inherit the setting). The toggle sits in the pane-control cluster.
  const { setPaneWrap } = useApp();
  const settings = useSettings();
  const wrapOn = pane.wrap ?? settings.wrap;
  const canWrap = pane.kind === "file" || pane.kind === "scm" || pane.kind === "diff";

  const onDragStart = (e) => {
    e.dataTransfer.setData(DND, pane.id);
    e.dataTransfer.effectAllowed = "move";
  };
  // Outer 30% of the splittable edges is a split zone; the center swaps. A split
  // edge is only offered when this pane can grow that way (else it stays center).
  const zoneFor = (e) => {
    const r = e.currentTarget.getBoundingClientRect();
    const rd = canSplitRight ? (e.clientX - r.left) / r.width - 0.7 : -1;
    const dd = canSplitDown ? (e.clientY - r.top) / r.height - 0.7 : -1;
    if (rd < 0 && dd < 0) return "center";
    return dd > rd ? "down" : "right";
  };
  const onDragOver = (e) => {
    if (!canDrag || !e.dataTransfer.types.includes(DND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const z = zoneFor(e);
    setZone((prev) => (prev === z ? prev : z));
  };
  const onDragLeave = (e) => {
    // Ignore bubbling from descendants; only clear when leaving the pane itself.
    if (e.currentTarget.contains(e.relatedTarget)) return;
    setZone(null);
  };
  const onDrop = (e) => {
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
        {canSplitRight && (
          <button type="button" className="ghost pane-btn" title="右に分割" onClick={onSplitRight}>
            <Icon name="split-horizontal" />
          </button>
        )}
        {canSplitDown && (
          <button
            type="button"
            className="ghost pane-btn"
            title="上下に分割"
            onClick={() => onSplitDown(pane.id)}
          >
            <Icon name="split-vertical" />
          </button>
        )}
        {canClose && (
          <button
            type="button"
            className="ghost pane-btn"
            title="このペインを閉じる"
            onClick={() => onClose(pane.id)}
          >
            <Icon name="close" />
          </button>
        )}
      </div>

      {/* Drop hint while dragging a pane over this one: a full-pane ring for a
          swap (center), or a half-pane box on the edge where the new split lands. */}
      {zone && <div className={"drop-indicator zone-" + zone} />}

      {/* Terminal is always mounted while the pane exists; hidden when showing
          another kind (or the Markdown mirror) so its socket + scrollback persist. */}
      <div className="view" hidden={!isTerm || showMirror}>
        <TerminalView
          paneId={pane.id}
          session={pane.session}
          sessionMeta={sessionMeta}
          active={(single || active) && isTerm && !showMirror}
          canMirror={canMirror}
          mirror={mirror}
          onToggleMirror={setMirror}
        />
      </div>
      {showMirror && (
        <MirrorView
          session={pane.session}
          sessionMeta={sessionMeta}
          active={single || active}
          mirror={mirror}
          onToggleMirror={setMirror}
        />
      )}
      {pane.kind === "scm" && <SourceControlView repo={pane.scmRepo} wrap={wrapOn} />}
      {pane.kind === "file" && <FileView filePath={pane.filePath} wrap={wrapOn} />}
      {pane.kind === "doc" && <DocView title={pane.docTitle} content={pane.docContent} />}
      {pane.kind === "diff" && (
        <DiffView title={pane.docTitle} tool={pane.diffTool} edits={pane.diffEdits} wrap={wrapOn} />
      )}
    </div>
  );
}
