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
}) {
  const isTerm = pane.kind === "terminal";
  const [dropping, setDropping] = useState(false);

  const onDragStart = (e) => {
    e.dataTransfer.setData(DND, pane.id);
    e.dataTransfer.effectAllowed = "move";
  };
  const onDragOver = (e) => {
    if (!canDrag || !e.dataTransfer.types.includes(DND)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    if (!dropping) setDropping(true);
  };
  const onDragLeave = (e) => {
    // Ignore bubbling from descendants; only clear when leaving the pane itself.
    if (e.currentTarget.contains(e.relatedTarget)) return;
    setDropping(false);
  };
  const onDrop = (e) => {
    if (!e.dataTransfer.types.includes(DND)) return;
    e.preventDefault();
    setDropping(false);
    const src = e.dataTransfer.getData(DND);
    if (src) onSwap(src, pane.id);
  };

  return (
    <div
      className={"pane" + (active ? " active" : "") + (dropping ? " droptarget" : "")}
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

      {/* Terminal is always mounted while the pane exists; hidden when showing
          another kind so its socket + scrollback persist. */}
      <div className="view" hidden={!isTerm}>
        <TerminalView paneId={pane.id} session={pane.session} active={(single || active) && isTerm} />
      </div>
      {pane.kind === "scm" && <SourceControlView repo={pane.scmRepo} />}
      {pane.kind === "file" && <FileView filePath={pane.filePath} />}
    </div>
  );
}
