// DocView — in-memory Markdown (e.g. a plan) in its own pane, no file on disk.
// The content lives in the pane descriptor. Port of views/DocView.
import type { CSSProperties } from "react";
import { MarkdownView } from "./MarkdownView.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";

interface DocViewProps {
  title?: string;
  content?: string;
}

export function DocView({ title, content }: DocViewProps) {
  const openTarget = useLayoutStore((s) => s.openTarget);
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
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
        <MarkdownView
          source={content || ""}
          onOpenFile={(p) => openTarget({ content: { kind: "file", filePath: p } })}
          onOpenDir={revealInFiles}
        />
      </div>
    </div>
  );
}
