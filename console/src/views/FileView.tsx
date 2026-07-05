import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties } from "react";
import hljs from "highlight.js/lib/common";
// Syntax theme is defined in styles.css via CSS variables so it follows the app
// theme (the github-dark.css import was dark-only → unreadable in light mode).
import { useApp } from "../state.jsx";
import { api, downloadURL } from "../api.js";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc, imageFormat } from "../lib/filemeta.js";
import FileIcon from "../components/FileIcon.jsx";
import Icon from "../components/Icon.jsx";
import { useSettings, fontStack } from "../lib/settings.js";
import MarkdownView from "./MarkdownView.jsx";
import MarpView from "./MarpView.jsx";
import CodeView from "./CodeView.jsx";
import ImageView from "./ImageView.jsx";
import SendSelectionModal from "../components/SendSelectionModal.jsx";

// lineRangeOfSelection derives the 1-based line range + text of the current selection
// within the code grid. Each code cell carries data-ln (its 1-based logical line), so
// the selection's endpoints map to line numbers by walking up to their cell — wrap- and
// highlight-agnostic (it reads data-ln, not DOM text lines). Returns null if the
// selection is empty or not inside root.
function lineRangeOfSelection(root: Element): { quote: string; startLine: number; endLine: number } | null {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
  const range = sel.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const quote = range.toString();
  if (!quote.trim()) return null;
  let a = lineNoOf(range.startContainer, root);
  let b = lineNoOf(range.endContainer, root);
  a = a ?? b;
  b = b ?? a;
  if (a == null || b == null) return null;
  return { quote, startLine: Math.min(a, b), endLine: Math.max(a, b) };
}

// Walk up from a selection endpoint to the nearest code cell and read its 1-based line
// number (data-ln). Returns null if the node isn't inside a code cell.
function lineNoOf(node: Node, root: Element): number | null {
  let el: HTMLElement | null = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
  while (el && el !== root) {
    if (el.dataset && el.dataset.ln) return parseInt(el.dataset.ln, 10);
    el = el.parentElement;
  }
  return null;
}

// FileView shows a single file (read-only) with CodeLeaf-style affordances: an info
// bar (name / language / size / line count / truncation) and syntax-highlighted
// content with a line-number gutter. Binary files report a summary, not bytes.
// filePath comes from the owning pane's descriptor; markdown link navigation opens
// in the active pane via the context showFile (falls back to context filePath when
// rendered standalone).
interface FileViewProps {
  filePath?: string | null;
  wrap?: boolean | null;
}

export default function FileView({ filePath: filePathProp, wrap }: FileViewProps) {
  const { filePath: ctxFilePath, showFile } = useApp();
  const filePath: string = (filePathProp !== undefined ? filePathProp : ctxFilePath) || "";
  const settings = useSettings();
  // wrap is a per-pane override (from the pane's toolbar toggle); fall back to the
  // global setting when the pane doesn't force it either way.
  const wrapOn = wrap === undefined || wrap === null ? settings.wrap : wrap;
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState("");
  const [mdMode, setMdMode] = useState<"preview" | "source" | "slides">("preview");
  const [imgMode, setImgMode] = useState<"preview" | "source">("preview");
  const [imgDims, setImgDims] = useState<{ w: number; h: number } | null>(null); // reported by ImageView
  const [marks, setMarks] = useState<any>(null); // git change marks for the gutter (repos/* only)
  // Quote-to-session/assistant (a selection in the code/source view → send excerpt).
  const bodyRef = useRef<HTMLDivElement>(null);
  const [sel, setSel] = useState<{ quote: string; startLine: number; endLine: number; x: number; y: number } | null>(null);
  const [sendOpen, setSendOpen] = useState(false);

  const imgFmt = imageFormat(filePath); // "" unless filePath is a previewable image
  const isImage = !!imgFmt;

  // Fetch editor-style change marks for git-tracked working-tree files; the
  // viewer draws a change bar from them. Non-repo paths clear the marks.
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

  const isText = data && !data.binary && typeof data.content === "string";
  const lines = isText ? countLines(data.content) : 0;
  const isMarkdown = isText && langFor(filePath) === "markdown";
  const isMarp = isMarkdown && isMarpDoc(data.content);

  // Default a freshly opened Marp deck to the slides view; other markdown to preview.
  useEffect(() => {
    if (isText) setMdMode(isMarp ? "slides" : "preview");
  }, [data, isMarp, isText]);

  const showSlides = isMarp && mdMode === "slides";
  const showPreview = isMarkdown && mdMode === "preview";

  // Highlight once per file load. Fall back to escaped plain text if the language
  // is unknown or highlighting throws.
  const html = useMemo(() => {
    if (!isText) return "";
    const lang = langFor(filePath);
    try {
      if (lang && hljs.getLanguage(lang)) {
        return hljs.highlight(data.content, { language: lang, ignoreIllegals: true }).value;
      }
    } catch {}
    return escapeHtml(data.content);
  }, [isText, data, filePath]);

  // After a mouse selection in the code/source view, surface a floating "送る" pill by
  // the selection. Scoped to CodeView because it queries that view's <code> element
  // (absent in md-preview / slides / image), so it stays inert elsewhere.
  const captureSelection = () => {
    const codeEl = bodyRef.current?.querySelector(".codeview .codegrid");
    if (!codeEl) return;
    const r = lineRangeOfSelection(codeEl);
    if (!r) {
      setSel(null);
      return;
    }
    const rect = window.getSelection()!.getRangeAt(0).getBoundingClientRect();
    setSel({ ...r, x: Math.round(rect.left), y: Math.round(rect.top - 34) });
  };

  if (!filePath) return <div className="fileview" />;

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
    "--viewer-tab": settings.tabSize,
  } as CSSProperties;

  return (
    <div className="fileview" style={viewerStyle} ref={bodyRef} onMouseUp={captureSelection}>
      <header className="view-head fileinfo">
        <span className="fi-name mono"><FileIcon name={baseName(filePath)} /> {baseName(filePath)}</span>
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
          <span className="seg sm md-toggle">
            {isMarp && (
              <button
                type="button"
                className={"seg-btn" + (mdMode === "slides" ? " active" : "")}
                onClick={() => setMdMode("slides")}
              >
                スライド
              </button>
            )}
            <button
              type="button"
              className={"seg-btn" + (mdMode === "preview" ? " active" : "")}
              onClick={() => setMdMode("preview")}
            >
              プレビュー
            </button>
            <button
              type="button"
              className={"seg-btn" + (mdMode === "source" ? " active" : "")}
              onClick={() => setMdMode("source")}
            >
              ソース
            </button>
          </span>
        )}
        {isImage && isText && (
          <span className="seg sm md-toggle">
            <button
              type="button"
              className={"seg-btn" + (imgMode === "preview" ? " active" : "")}
              onClick={() => setImgMode("preview")}
            >
              プレビュー
            </button>
            <button
              type="button"
              className={"seg-btn" + (imgMode === "source" ? " active" : "")}
              onClick={() => setImgMode("source")}
            >
              ソース
            </button>
          </span>
        )}
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
        <a
          className="ghost fi-dl"
          href={downloadURL(filePath)}
          download={baseName(filePath)}
          title="ダウンロード"
        >
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
          <MarkdownView source={data.content} basePath={filePath} onOpenFile={showFile} />
        </div>
      ) : (
        <CodeView
          html={html}
          lines={lines}
          lineNumbers={settings.lineNumbers}
          wrap={wrapOn}
          minimap={settings.minimap}
          marks={marks}
        />
      )}

      {/* Portal to <body>: .fileview is a CSS container (container-type), which makes it
          the containing block for position:fixed descendants — a pill/modal rendered
          inside would be positioned relative to the pane, not the viewport. */}
      {sel &&
        !sendOpen &&
        createPortal(
          <button
            type="button"
            className="sel-send-pill"
            style={{ left: sel.x, top: Math.max(4, sel.y) }}
            onMouseDown={(e) => e.preventDefault()} // keep the text selection alive through the click
            onClick={() => setSendOpen(true)}
          >
            <Icon name="comment-discussion" /> 送る
          </button>,
          document.body,
        )}
      {sendOpen &&
        sel &&
        createPortal(
          <SendSelectionModal
            filePath={filePath}
            quote={sel.quote}
            startLine={sel.startLine}
            endLine={sel.endLine}
            onClose={() => {
              setSendOpen(false);
              setSel(null);
            }}
          />,
          document.body,
        )}
    </div>
  );
}

function escapeHtml(s: string) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
