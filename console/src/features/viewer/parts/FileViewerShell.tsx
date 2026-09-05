// The reading surface (.file-viewer-shell). A single branch decides which surface to render
// from the file's kind and the selected mode: image / PDF / Office document / binary /
// oversized text / slides / Markdown preview / code.
//
// The branch order is significant, so the conditions stay as one chain of ternaries. It
// holds no hooks; all state lives in FileView.
import { downloadURL } from "../../../core/api/client.ts";
import { baseName, countLines, humanSize } from "../../../lib/filemeta.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { CodeView, type LineMarks } from "../CodeView.tsx";
import { DocPreview } from "../DocPreview.tsx";
import { ImageView } from "../ImageView.tsx";
import { MarkdownView } from "../MarkdownView.tsx";
import { MarpView } from "../MarpView.tsx";
import { PdfView } from "../PdfView.tsx";
import type { FileSurfaces } from "../fileMode.ts";
import type { ScrollMemoryFactory } from "./useScrollMemory.ts";

export interface FileViewerShellProps {
  hidden: boolean;
  filePath: string;
  err: string;
  /** false = not read yet (renders the ellipsis placeholder). */
  loaded: boolean;
  size?: number;
  binary?: boolean;
  isImage: boolean;
  isText: boolean;
  imgMode: "preview" | "source";
  onImgDims(dims: { w: number; h: number }): void;
  isPdf: boolean;
  onPdfMeta(meta: { pages: number }): void;
  isDoc: boolean;
  docFmt?: string;
  onOpenFile(path: string, line?: number, column?: number, openInNew?: boolean): void;
  onOpenDir(path: string): void;
  huge: boolean;
  viewContent: string;
  preview: FileSurfaces["preview"];
  previewSource: string;
  html: string;
  lineNumbers: boolean;
  wrap: boolean;
  minimap: boolean;
  marks: LineMarks | null;
  targetLine?: number;
  targetColumn?: number;
  /** Hands out the scroll-position-memory ref per surface (owned by FileView). A different
   *  surface means a different memory, so each branch asks for a different name. */
  scrollMemory: ScrollMemoryFactory;
}

export function FileViewerShell(props: FileViewerShellProps) {
  const tr = useT();
  const { filePath, isImage, isText, imgMode, isPdf, isDoc } = props;
  return (
    <div className="file-viewer-shell" hidden={props.hidden}>
      {props.err ? (
        <pre className="filebody muted">({props.err})</pre>
      ) : !props.loaded ? (
        <pre className="filebody muted">…</pre>
      ) : isImage && (!isText || imgMode === "preview") ? (
        <ImageView src={downloadURL(filePath)} alt={baseName(filePath)} onLoad={props.onImgDims} />
      ) : isPdf ? (
        // PDF is caught one step before "binary" (docs/log/82). api/fs/file returns no
        // content, only binary:true, so from here on the raw bytes from download are the
        // source of truth.
        <PdfView src={downloadURL(filePath)} onMeta={props.onPdfMeta} scrollMemory={props.scrollMemory("pdf")} />
      ) : isDoc ? (
        <DocPreview
          src={downloadURL(filePath)}
          format={props.docFmt}
          size={props.size}
          basePath={filePath}
          onOpenFile={props.onOpenFile}
          onOpenDir={props.onOpenDir}
          scrollMemory={props.scrollMemory("doc")}
        />
      ) : props.binary ? (
        <pre className="filebody muted">({tr("view.binary")}, {humanSize(props.size)})</pre>
      ) : props.huge ? (
        <pre className="filebody fb-plain" ref={props.scrollMemory("plain")}>{props.viewContent}</pre>
      ) : props.preview === "slides" ? (
        <MarpView source={props.previewSource} />
      ) : props.preview === "normal" ? (
        <div className="md-scroll" ref={props.scrollMemory("preview")}>
          <MarkdownView source={props.previewSource} basePath={filePath} onOpenFile={props.onOpenFile} onOpenDir={props.onOpenDir} />
        </div>
      ) : (
        <CodeView
          html={props.html}
          lines={countLines(props.viewContent)}
          lineNumbers={props.lineNumbers}
          wrap={props.wrap}
          minimap={props.minimap}
          marks={props.marks}
          targetLine={props.targetLine}
          targetColumn={props.targetColumn}
          scrollMemory={props.scrollMemory("code")}
        />
      )}
    </div>
  );
}
