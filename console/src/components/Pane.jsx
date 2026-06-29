import TerminalView from "../views/TerminalView.jsx";
import SourceControlView from "../views/SourceControlView.jsx";
import FileView from "../views/FileView.jsx";
import Icon from "./Icon.jsx";

// Pane renders one slot of the main-area layout. Like the original single-pane app,
// the terminal stays mounted (just hidden) while the pane shows a file/scm view, so
// the PTY socket and scrollback survive switching kinds. The file/scm views overlay
// on top when active. A small control cluster (split / close) sits at the top-right;
// a mousedown anywhere makes this the active pane (clicks then open here).
export default function Pane({ pane, active, split, onActivate, onSplit, onClose }) {
  const isTerm = pane.kind === "terminal";
  // The active outline only reads as meaningful when split; a single pane is always
  // "the" pane, so we don't ring it. `active` still drives terminal fit/focus.
  const ring = active && split !== "single";
  return (
    <div
      className={"pane" + (ring ? " active" : "")}
      onMouseDownCapture={() => onActivate(pane.id)}
    >
      <div className="pane-controls">
        {split === "single" ? (
          <button type="button" className="ghost pane-btn" title="右に分割" onClick={onSplit}>
            <Icon name="split-horizontal" />
          </button>
        ) : (
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
        <TerminalView paneId={pane.id} session={pane.session} active={active && isTerm} />
      </div>
      {pane.kind === "scm" && <SourceControlView repo={pane.scmRepo} />}
      {pane.kind === "file" && <FileView filePath={pane.filePath} />}
    </div>
  );
}
