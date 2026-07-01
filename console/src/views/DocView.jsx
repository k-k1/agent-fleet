import MarkdownView from "./MarkdownView.jsx";
import Icon from "../components/Icon.jsx";
import { useApp } from "../state.jsx";
import { useSettings, fontStack } from "../lib/settings.js";

// DocView renders in-memory Markdown (e.g. a plan) in its own pane — no file on disk.
// The content lives in the pane descriptor (docTitle / docContent). Reuses the file
// viewer's chrome + markdown styles.
export default function DocView({ title, content }) {
  const { showFile } = useApp();
  const settings = useSettings();
  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
  };
  return (
    <div className="fileview docview" style={viewerStyle}>
      <header className="view-head fileinfo">
        <span className="fi-name mono">
          <Icon name="checklist" /> {title || "ドキュメント"}
        </span>
        <span className="fi-tag">Markdown</span>
      </header>
      <div className="md-scroll">
        <MarkdownView source={content || ""} onOpenFile={showFile} />
      </div>
    </div>
  );
}
