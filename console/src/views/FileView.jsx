import { useEffect, useMemo, useState } from "react";
import hljs from "highlight.js/lib/common";
// Syntax theme is defined in styles.css via CSS variables so it follows the app
// theme (the github-dark.css import was dark-only → unreadable in light mode).
import { useApp } from "../state.jsx";
import { api } from "../api.js";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc } from "../lib/filemeta.js";
import FileIcon from "../components/FileIcon.jsx";
import { useSettings, fontStack } from "../lib/settings.js";
import MarkdownView from "./MarkdownView.jsx";
import MarpView from "./MarpView.jsx";
import CodeView from "./CodeView.jsx";

// FileView shows a single file (read-only) with CodeLeaf-style affordances: an info
// bar (name / language / size / line count / truncation) and syntax-highlighted
// content with a line-number gutter. Binary files report a summary, not bytes.
export default function FileView() {
  const { filePath, showFile } = useApp();
  const settings = useSettings();
  const [data, setData] = useState(null);
  const [err, setErr] = useState("");
  const [mdMode, setMdMode] = useState("preview"); // markdown only: 'preview' | 'source' | 'slides'

  useEffect(() => {
    if (!filePath) return;
    let alive = true;
    setData(null);
    setErr("");
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

  if (!filePath) return <div className="fileview" />;

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
    "--viewer-tab": settings.tabSize,
  };

  return (
    <div className="fileview" style={viewerStyle}>
      <header className="view-head fileinfo">
        <span className="fi-name mono"><FileIcon name={baseName(filePath)} /> {baseName(filePath)}</span>
        {isText && <span className="fi-tag">{langLabel(filePath)}</span>}
        <span className="fi-meta muted">
          {humanSize(data?.size)}
          {isText ? ` · ${lines} 行` : ""}
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
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
      </header>

      {err ? (
        <pre className="filebody muted">({err})</pre>
      ) : data == null ? (
        <pre className="filebody muted">…</pre>
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
          wrap={settings.wrap}
          minimap={settings.minimap}
        />
      )}
    </div>
  );
}

function escapeHtml(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
