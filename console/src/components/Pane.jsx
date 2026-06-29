import { useState } from "react";
import TerminalView from "../views/TerminalView.jsx";
import SourceControlView from "../views/SourceControlView.jsx";
import FileView from "../views/FileView.jsx";
import Icon from "./Icon.jsx";

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
}) {
  const isTerm = pane.kind === "terminal";
  // null when not a drop target; otherwise the zone the pointer is in:
  //   'center' → swap with the dragged pane; 'right'/'down' → tear the dragged
  //   pane off into a new split (new right column / downward split of this column).
  const [zone, setZone] = useState(null);

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
      className={"pane" + (active ? " active" : "") + (zone ? " droptarget" : "")}
      onMouseDownCapture={() => onActivate(pane.id)}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <div className="pane-controls">
        {canDrag && (
          <button
            type="button"
            className="ghost pane-btn pane-grip"
            title="ドラッグして他のペインと入れ替え"
            draggable
            onDragStart={onDragStart}
          >
            <Icon name="gripper" />
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
          another kind so its socket + scrollback persist. */}
      <div className="view" hidden={!isTerm}>
        <TerminalView paneId={pane.id} session={pane.session} sessionMeta={sessionMeta} active={(single || active) && isTerm} />
      </div>
      {pane.kind === "scm" && <SourceControlView repo={pane.scmRepo} />}
      {pane.kind === "file" && <FileView filePath={pane.filePath} />}
    </div>
  );
}
