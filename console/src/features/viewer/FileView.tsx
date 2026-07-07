// FileView — a single file (read-only) with CodeLeaf-style affordances: info bar
// (name / language / size / lines / truncation), syntax-highlighted code with a
// gutter + minimap + git change bar, markdown preview/source/slides toggle,
// image preview. Port of views/FileView onto the zustand stores.
//
// TODO(P6): the selection → 送る pill (SendSelectionModal) returns with the memo
// queue feature.
import { useEffect, useMemo, useState } from "react";
import type { CSSProperties } from "react";
import hljs from "highlight.js/lib/common";
import { api, downloadURL } from "../../core/api/client.ts";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc, imageFormat } from "../../lib/filemeta.ts";
import FileIcon from "../../components/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { MarpView } from "./MarpView.tsx";
import { CodeView } from "./CodeView.tsx";
import { ImageView } from "./ImageView.tsx";
import type { LineMarks } from "./CodeView.tsx";

interface FileViewProps {
  filePath: string;
  wrap?: boolean | null;
}

interface FileData {
  error?: { message?: string };
  binary?: boolean;
  content?: string;
  size?: number;
  truncated?: boolean;
  lfs?: boolean;
}

export function FileView({ filePath, wrap }: FileViewProps) {
  const openTarget = useLayoutStore((s) => s.openTarget);
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
  const settings = useSettings();
  // wrap is the per-pane override; fall back to the global setting.
  const wrapOn = wrap === undefined || wrap === null ? settings.wrap : wrap;
  const [data, setData] = useState<FileData | null>(null);
  const [err, setErr] = useState("");
  const [mdMode, setMdMode] = useState<"preview" | "source" | "slides">("preview");
  const [imgMode, setImgMode] = useState<"preview" | "source">("preview");
  const [imgDims, setImgDims] = useState<{ w: number; h: number } | null>(null);
  const [marks, setMarks] = useState<LineMarks | null>(null);

  const showFile = (path: string) => openTarget({ content: { kind: "file", filePath: path } });

  const imgFmt = imageFormat(filePath);
  const isImage = !!imgFmt;

  // Editor-style change marks for git-tracked working-tree files.
  useEffect(() => {
    setMarks(null);
    if (!filePath || !filePath.startsWith("repos/")) return;
    let alive = true;
    api(`api/fs/linemarks?path=${encodeURIComponent(filePath)}`)
      .then((d) => alive && d && !d.error && setMarks(d))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [filePath]);

  useEffect(() => {
    if (!filePath) return;
    let alive = true;
    setData(null);
    setErr("");
    setImgDims(null);
    setImgMode("preview");
    api(`api/fs/file?path=${encodeURIComponent(filePath)}`)
      .then((d) => {
        if (!alive) return;
        if (d && d.error) setErr(d.error.message || "読み込めません");
        else setData(d);
      })
      .catch(() => alive && setErr("読み込めません"));
    return () => {
      alive = false;
    };
  }, [filePath]);

  const isText = !!data && !data.binary && typeof data.content === "string";
  const lines = isText ? countLines(data!.content!) : 0;
  const isMarkdown = isText && langFor(filePath) === "markdown";
  const isMarp = isMarkdown && isMarpDoc(data!.content);

  // A freshly opened Marp deck defaults to slides; other markdown to preview.
  useEffect(() => {
    if (isText) setMdMode(isMarp ? "slides" : "preview");
  }, [data, isMarp, isText]);

  const showSlides = isMarp && mdMode === "slides";
  const showPreview = isMarkdown && mdMode === "preview";

  // Highlight once per file load; fall back to escaped plain text.
  const html = useMemo(() => {
    if (!isText) return "";
    const lang = langFor(filePath);
    try {
      if (lang && hljs.getLanguage(lang)) {
        return hljs.highlight(data!.content!, { language: lang, ignoreIllegals: true }).value;
      }
    } catch {}
    return escapeHtml(data!.content!);
  }, [isText, data, filePath]);

  if (!filePath) return <div className="fileview" />;

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
    "--viewer-tab": settings.tabSize,
  } as CSSProperties;

  return (
    <div className="fileview" style={viewerStyle}>
      <header className="view-head fileinfo">
        <span className="fi-name mono">
          <FileIcon name={baseName(filePath)} /> {baseName(filePath)}
        </span>
        {isImage ? (
          <span className="fi-tag">{imgFmt.toUpperCase()}</span>
        ) : isText ? (
          <span className="fi-tag">{langLabel(filePath)}</span>
        ) : null}
        <span className="fi-meta muted">
          {humanSize(data?.size)}
          {isImage && imgDims ? ` · ${imgDims.w}×${imgDims.h}` : ""}
          {!isImage && isText ? ` · ${lines} 行` : ""}
          {data?.truncated ? " · 先頭のみ" : ""}
        </span>
        {data?.lfs && (
          <span className="fi-tag" title="Git LFS の実体は未取得です。端末で `git lfs pull` を実行してください。">
            LFS ポインタ
          </span>
        )}
        {isMarkdown && (
          <span className="ui-seg sm md-toggle">
            {isMarp && (
              <button type="button" className={"seg-btn" + (mdMode === "slides" ? " active" : "")} onClick={() => setMdMode("slides")}>
                スライド
              </button>
            )}
            <button type="button" className={"seg-btn" + (mdMode === "preview" ? " active" : "")} onClick={() => setMdMode("preview")}>
              プレビュー
            </button>
            <button type="button" className={"seg-btn" + (mdMode === "source" ? " active" : "")} onClick={() => setMdMode("source")}>
              ソース
            </button>
          </span>
        )}
        {isImage && isText && (
          <span className="ui-seg sm md-toggle">
            <button type="button" className={"seg-btn" + (imgMode === "preview" ? " active" : "")} onClick={() => setImgMode("preview")}>
              プレビュー
            </button>
            <button type="button" className={"seg-btn" + (imgMode === "source" ? " active" : "")} onClick={() => setImgMode("source")}>
              ソース
            </button>
          </span>
        )}
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
        <a className="fi-dl" href={downloadURL(filePath)} download={baseName(filePath)} title="ダウンロード">
          <Icon name="cloud-download" />
        </a>
      </header>

      {err ? (
        <pre className="filebody muted">({err})</pre>
      ) : data == null ? (
        <pre className="filebody muted">…</pre>
      ) : isImage && (!isText || imgMode === "preview") ? (
        <ImageView src={downloadURL(filePath)} alt={baseName(filePath)} onLoad={setImgDims} />
      ) : data.binary ? (
        <pre className="filebody muted">(バイナリ, {humanSize(data.size)})</pre>
      ) : showSlides ? (
        <MarpView source={data.content} />
      ) : showPreview ? (
        <div className="md-scroll">
          <MarkdownView source={data.content} basePath={filePath} onOpenFile={showFile} onOpenDir={revealInFiles} />
        </div>
      ) : (
        <CodeView html={html} lines={lines} lineNumbers={settings.lineNumbers} wrap={wrapOn} minimap={settings.minimap} marks={marks} />
      )}
    </div>
  );
}

function escapeHtml(s: string) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
