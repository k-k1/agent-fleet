// FileView — a single file (read-only) with CodeLeaf-style affordances: info bar
// (name / language / size / lines / truncation), syntax-highlighted code with a
// gutter + minimap + git change bar, markdown preview/source/slides toggle,
// image preview, and the selection → 送る pill (SendSelectionModal). Port of
// views/FileView onto the zustand stores.
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties } from "react";
import { SendSelectionModal } from "../memo/SendSelectionModal.tsx";
import hljs from "highlight.js/lib/common";
import { api, downloadURL } from "../../core/api/client.ts";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc, imageFormat } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { speakText, startNarration, type NarrationHandle } from "../chat/tts.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { MarpView } from "./MarpView.tsx";
import { CodeView } from "./CodeView.tsx";
import { ImageView } from "./ImageView.tsx";
import type { LineMarks } from "./CodeView.tsx";

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
  const bodyRef = useRef<HTMLDivElement>(null);
  const [sel, setSel] = useState<{ quote: string; startLine: number; endLine: number; x: number; y: number } | null>(null);
  const [sendOpen, setSendOpen] = useState(false);
  // 朗読モード（docs/24）: 冒頭から順次読み上げ＋読んでいるブロックをハイライト追従。
  const [narrOn, setNarrOn] = useState(false);
  const [narrPaused, setNarrPaused] = useState(false);
  const [pendingNarr, setPendingNarr] = useState(false); // preview へ切替後に開始するための保留
  const narrRef = useRef<{ handle: NarrationHandle; els: HTMLElement[] } | null>(null);

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
  // Large-file mode: a huge file (or one with enormous single lines, e.g. embedded
  // data-URI images in a generated md) breaks the per-line grid + highlighter —
  // the line-number tracks balloon and the body renders blank. Fall back to a
  // plain scrollable <pre>: readable, cheap, and honest about the size.
  const huge = useMemo(() => {
    if (!isText) return false;
    const c = data!.content!;
    if (c.length > 300_000 || lines > 20_000) return true;
    // any single line longer than ~4000 chars (minified / data URIs)
    let start = 0;
    for (;;) {
      const nl = c.indexOf("\n", start);
      const end = nl === -1 ? c.length : nl;
      if (end - start > 4000) return true;
      if (nl === -1) return false;
      start = nl + 1;
    }
  }, [isText, data, lines]);
  const isMarkdown = isText && !huge && langFor(filePath) === "markdown";
  const isMarp = isMarkdown && isMarpDoc(data!.content);

  // A freshly opened Marp deck defaults to slides; other markdown to preview.
  useEffect(() => {
    if (isText) setMdMode(isMarp ? "slides" : "preview");
  }, [data, isMarp, isText]);

  const showSlides = isMarp && mdMode === "slides";
  const showPreview = isMarkdown && mdMode === "preview";

  // Highlight once per file load; fall back to escaped plain text. Huge files
  // skip highlighting entirely (plain mode below).
  const html = useMemo(() => {
    if (!isText || huge) return "";
    const lang = langFor(filePath);
    try {
      if (lang && hljs.getLanguage(lang)) {
        return hljs.highlight(data!.content!, { language: lang, ignoreIllegals: true }).value;
      }
    } catch {}
    return escapeHtml(data!.content!);
  }, [isText, huge, data, filePath]);

  // After a mouse selection in the code/source view, surface a floating "送る" pill by
  // the selection. Scoped to CodeView because it queries that view's <code> element
  // (absent in md-preview / slides / image), so it stays inert elsewhere.
  const captureSelection = () => {
    // While the send modal is open, ignore mouseups — React portals bubble events through
    // the React tree, so a click inside the (body-portaled) modal reaches this handler and
    // would clear `sel` (the modal is gated on it), closing the modal on the first click.
    if (sendOpen) return;
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

  // Touch text-selection (long-press + drag handles on mobile) does NOT fire mouseup/
  // keyup, so the pill never appeared on phones. `selectionchange` fires for touch too;
  // debounce it (selection updates continuously while dragging the handles) and reuse the
  // same capture. Keep a ref so the mount-once listener always calls the latest closure
  // (captureSelection closes over sendOpen). captureSelection itself is scoped to this
  // view's codegrid, so selections elsewhere just clear our pill.
  const captureRef = useRef(captureSelection);
  captureRef.current = captureSelection;
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | null = null;
    const onSelChange = () => {
      if (t) clearTimeout(t);
      t = setTimeout(() => captureRef.current(), 250);
    };
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      if (t) clearTimeout(t);
    };
  }, []);

  // --- 朗読モード（docs/24） ------------------------------------------------------
  // 朗読の対象ブロックを DOM から集める。Markdown プレビューは葉ブロック（p/li/見出し等、
  // コードは除く）、コード/プレーン表示は行（.cl-code[data-ln]）。各要素をハイライトに使う。
  const collectNarrationUnits = (): HTMLElement[] => {
    const body = bodyRef.current;
    if (!body) return [];
    const md = body.querySelector(".markdown");
    if (md) {
      const cand = Array.from(md.querySelectorAll<HTMLElement>("p,li,h1,h2,h3,h4,h5,h6,blockquote,dd,dt,td,th,figcaption"));
      return cand.filter((el) => {
        if (!(el.textContent || "").trim()) return false;
        if (el.closest("pre")) return false; // コードブロックは読まない
        for (const other of cand) if (other !== el && el.contains(other)) return false; // 入れ子は内側だけ（重複読み防止）
        return true;
      });
    }
    const grid = body.querySelector(".codegrid");
    if (grid) return Array.from(grid.querySelectorAll<HTMLElement>(".cl-code[data-ln]")).filter((el) => (el.textContent || "").trim() !== "");
    return [];
  };

  const clearNarrHighlight = () => narrRef.current?.els.forEach((e) => e.classList.remove("tts-reading"));

  const beginNarration = () => {
    const els = collectNarrationUnits();
    if (!els.length) return;
    const handle = startNarration(
      els.map((el) => el.textContent || ""),
      baseName(filePath),
      (i) => {
        clearNarrHighlight();
        if (i != null && els[i]) {
          els[i].classList.add("tts-reading");
          els[i].scrollIntoView({ block: "center", behavior: "smooth" });
        }
        if (i == null) {
          // 自然終了 or 外部（TopBar 停止・他の再生開始）で終了 → 状態リセット
          setNarrOn(false);
          setNarrPaused(false);
          narrRef.current = null;
        }
      },
    );
    narrRef.current = { handle, els };
    setNarrOn(true);
    setNarrPaused(false);
  };

  const startNarr = () => {
    // Markdown はプレビュー表示のブロックを対象にするため、ソース/スライドなら preview へ切替。
    if (isMarkdown && !showPreview) {
      setMdMode("preview");
      setPendingNarr(true);
      return;
    }
    beginNarration();
  };
  const stopNarr = () => narrRef.current?.handle.stop(); // onUnit(null) が解除・リセットを行う
  const togglePauseNarr = () => {
    const h = narrRef.current?.handle;
    if (!h) return;
    if (h.isPaused()) {
      h.resume();
      setNarrPaused(false);
    } else {
      h.pause();
      setNarrPaused(true);
    }
  };

  // プレビューへ切替後に朗読開始（DOM が出来てから集める）。
  useEffect(() => {
    if (pendingNarr && showPreview) {
      setPendingNarr(false);
      beginNarration();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingNarr, showPreview]);

  // ファイル変更・アンマウントで朗読停止（ハイライト先の DOM が消えるため）。
  useEffect(() => {
    return () => {
      narrRef.current?.handle.stop();
      narrRef.current = null;
    };
  }, [filePath]);

  // ソース/スライドへ切替＝プレビューの対象 DOM が消えるので朗読を止める。
  useEffect(() => {
    if (narrOn && isMarkdown && !showPreview) narrRef.current?.handle.stop();
  }, [showPreview, isMarkdown, narrOn]);

  if (!filePath) return <div className="fileview" />;

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
    "--viewer-tab": settings.tabSize,
  } as CSSProperties;

  return (
    <div className="fileview" style={viewerStyle} ref={bodyRef} onMouseUp={captureSelection} onKeyUp={captureSelection}>
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
        {huge && (
          <span className="fi-tag" title="巨大ファイル（または極端に長い行）のため、ハイライト・行番号なしのプレーン表示です">
            プレーン表示
          </span>
        )}
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
        {settings.ttsEnabled && isText && !huge && (
          <span className="ui-seg sm md-toggle tts-narrate">
            {!narrOn ? (
              <button type="button" className="seg-btn" onClick={startNarr} title="ファイルの冒頭から順次読み上げ（朗読）">
                <Icon name="unmute" /> 朗読
              </button>
            ) : (
              <>
                <button type="button" className="seg-btn" onClick={togglePauseNarr} title={narrPaused ? "再開" : "一時停止"}>
                  <Icon name={narrPaused ? "play" : "debug-pause"} /> {narrPaused ? "再開" : "一時停止"}
                </button>
                <button type="button" className="seg-btn active" onClick={stopNarr} title="朗読を停止">
                  <Icon name="debug-stop" /> 停止
                </button>
              </>
            )}
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
      ) : huge ? (
        <pre className="filebody fb-plain">{data.content}</pre>
      ) : showSlides ? (
        <MarpView source={data.content} />
      ) : showPreview ? (
        <div className="md-scroll">
          <MarkdownView source={data.content} basePath={filePath} onOpenFile={showFile} onOpenDir={revealInFiles} />
        </div>
      ) : (
        <CodeView html={html} lines={lines} lineNumbers={settings.lineNumbers} wrap={wrapOn} minimap={settings.minimap} marks={marks} />
      )}

      {/* Portal to <body>: .fileview is a CSS container (container-type), which makes it
          the containing block for position:fixed descendants — a pill/modal rendered
          inside would be positioned relative to the pane, not the viewport. */}
      {sel &&
        !sendOpen &&
        createPortal(
          <div className="sel-pill-group" style={{ left: sel.x, top: Math.max(4, sel.y) }}>
            <button
              type="button"
              className="sel-send-pill"
              onMouseDown={(e) => e.preventDefault()} // keep the text selection alive through the click
              onClick={() => setSendOpen(true)}
            >
              <Icon name="comment-discussion" /> 送る
            </button>
            {/* 音声読み上げが有効なときだけ、選択範囲を読み上げるピルを併置（docs/24）。 */}
            {settings.ttsEnabled && (
              <button
                type="button"
                className="sel-send-pill"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => speakText(sel.quote, "選択範囲")}
              >
                <Icon name="unmute" /> 読み上げ
              </button>
            )}
          </div>,
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
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
