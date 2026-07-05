import MarkdownView from "./MarkdownView.jsx";
import Icon from "../components/Icon.jsx";
import { useApp } from "../state.jsx";
import { useSettings, fontStack } from "../lib/settings.js";
import type { CSSProperties } from "react";

// DocView renders in-memory Markdown (e.g. a plan) in its own pane — no file on disk.
// The content lives in the pane descriptor (docTitle / docContent). Reuses the file
// viewer's chrome + markdown styles.
interface DocViewProps {
  title?: string;
  content?: string;
}

export default function DocView({ title, content }: DocViewProps) {
  const { showFile, revealInFiles } = useApp();
  const settings = useSettings();
  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
  } as CSSProperties;
  return (
    <div className="fileview docview" style={viewerStyle}>
      <header className="view-head fileinfo">
        <span className="fi-name mono">
          <Icon name="checklist" /> {title || "ドキュメント"}
        </span>
        <span className="fi-tag">Markdown</span>
      </header>
      <div className="md-scroll">
        <MarkdownView source={content || ""} onOpenFile={showFile} onOpenDir={revealInFiles} />
      </div>
    </div>
  );
}
