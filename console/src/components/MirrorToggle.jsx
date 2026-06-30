// Segmented ターミナル/チャット switch, shared by the terminal pane header and the
// mirror header so the control looks identical in both (like FileView's md-toggle).
// "mirror" is the internal name; the user-facing label is チャット. It is a
// read-mostly Markdown view of a claude session's assistant output
// (the same Agent /output + /input the MCP drive tools use), overlaid on the still-
// mounted terminal so the PTY socket survives switching.
export default function MirrorToggle({ mirror, onToggle }) {
  return (
    <span className="seg sm md-toggle mirror-toggle">
      <button
        type="button"
        className={"seg-btn" + (!mirror ? " active" : "")}
        onClick={() => onToggle(false)}
      >
        ターミナル
      </button>
      <button
        type="button"
        className={"seg-btn" + (mirror ? " active" : "")}
        onClick={() => onToggle(true)}
      >
        チャット
      </button>
    </span>
  );
}
