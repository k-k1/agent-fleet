import Icon from "./Icon.jsx";

// Segmented ターミナル/チャット switch, shared by the terminal pane header and the
// mirror header so the control looks identical in both (like FileView's md-toggle).
// "mirror" is the internal name; the user-facing label is チャット. It is a
// read-mostly Markdown view of a claude session's assistant output
// (the same Agent /output + /input the MCP drive tools use), overlaid on the still-
// mounted terminal so the PTY socket survives switching.
//
// Each button carries an icon + label. On a narrow pane (mobile or a slim split)
// the .seg-label collapses to icon-only via the paneview container query — see
// styles.css. The title keeps the full word reachable as a tooltip.
interface MirrorToggleProps {
  mirror: boolean;
  onToggle?: (toChat: boolean) => void;
  running?: boolean;
}

export default function MirrorToggle({ mirror, onToggle, running = true }: MirrorToggleProps) {
  return (
    <span className="seg sm md-toggle mirror-toggle">
      <button
        type="button"
        className={"seg-btn" + (mirror ? " active" : "")}
        title="チャット"
        onClick={() => onToggle?.(true)}
      >
        <Icon name="comment-discussion" />
        <span className="seg-label">チャット</span>
      </button>
      <button
        type="button"
        className={"seg-btn" + (!mirror ? " active" : "")}
        // Switching to the terminal attaches (resumes) the session, which needs the
        // workspace running. While it's stopped, disable it — otherwise the resume
        // mask would spin "再開中…" forever (attach is gated on running).
        title={running ? "ターミナル" : "ターミナル（ワークスペース停止中）"}
        disabled={!running}
        onClick={() => onToggle?.(false)}
      >
        <Icon name="terminal" />
        <span className="seg-label">ターミナル</span>
      </button>
    </span>
  );
}
