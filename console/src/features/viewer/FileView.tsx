// FileView — a single file (read-only) with CodeLeaf-style affordances: info bar
// (name / language / size / lines / truncation), syntax-highlighted code with a
// gutter + minimap + git change bar, markdown preview/source/slides toggle,
// image preview, and the selection → 送る pill (SendSelectionModal). Port of
// views/FileView onto the zustand stores.
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, KeyboardEvent } from "react";
import { SendSelectionModal } from "../memo/SendSelectionModal.tsx";
import hljs from "highlight.js/lib/common";
import { api, downloadURL, isTransientErr } from "../../core/api/client.ts";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc, imageFormat } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { speakText } from "../chat/tts.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { MarpView } from "./MarpView.tsx";
import { CodeView } from "./CodeView.tsx";
import { ImageView } from "./ImageView.tsx";
import { registerPaneViewActions } from "./paneViewActions.ts";
import type { LineMarks } from "./CodeView.tsx";
import { CodeEditor } from "../editor/CodeEditor.tsx";
import { useFileEditor } from "../editor/useFileEditor.ts";
import { revisionOf, type BufferValidationError } from "../editor/buffer.ts";
import type { FileEditorModel } from "../editor/model.ts";

type MdMode = "preview" | "source" | "slides";

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
  targetLine?: number;
  targetColumn?: number;
  wrap?: boolean | null;
  /** The host pane's id — lets global keyboard commands drive this view's local
   * Markdown preview/source toggle via the pane-view action registry. */
  paneId?: string;
}

interface FileData {
  error?: { message?: string };
  path?: string;
  binary?: boolean;
  content?: string;
  size?: number;
  truncated?: boolean;
  lfs?: boolean;
  editable?: boolean;
  editabilityReason?: string | null;
  revision?: string;
}

export function FileView({ filePath, targetLine, targetColumn, wrap, paneId }: FileViewProps) {
  const tr = useT();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
  const settings = useSettings();
  // wrap is the per-pane override; fall back to the global setting.
  const wrapOn = wrap === undefined || wrap === null ? settings.wrap : wrap;
  const [data, setData] = useState<FileData | null>(null);
  const [err, setErr] = useState("");
  const [mdMode, setMdMode] = useState<MdMode>("preview");
  const [imgMode, setImgMode] = useState<"preview" | "source">("preview");
  const [imgDims, setImgDims] = useState<{ w: number; h: number } | null>(null);
  const [marks, setMarks] = useState<LineMarks | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [sel, setSel] = useState<{ quote: string; startLine: number; endLine: number; x: number; y: number } | null>(null);
  const [sendOpen, setSendOpen] = useState(false);
  const [mode, setMode] = useState<"view" | "edit">("view");
  const [editorNotice, setEditorNotice] = useState("");
  const [resolutionOpen, setResolutionOpen] = useState(true);
  const viewTabRef = useRef<HTMLButtonElement>(null);
  const editTabRef = useRef<HTMLButtonElement>(null);
  const editorFocusRef = useRef<(() => void) | null>(null);

  const showFile = (path: string, line?: number, column?: number, openInNew = false) => {
    const target = { content: { kind: "file" as const, filePath: path, targetLine: line, targetColumn: column } };
    if (openInNew) openTargetInNew(target, true);
    else openTarget(target);
  };

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
    let timer = 0;
    let tries = 0;
    let settled = false; // a terminal result (content or a real error) has landed
    setData(null);
    setErr("");
    setImgDims(null);
    setImgMode("preview");
    // Load the file, retrying transient gateway failures. Right after a WS start the agent
    // is briefly unreachable and api() resolves an http_5xx error (not a throw); committing
    // that as a real error would leave the pane stuck on "(…cannot load)" forever. Genuine
    // errors (missing file, permission) carry an app code and stay terminal.
    const retry = () => {
      if (!alive) return;
      const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
      tries++;
      timer = window.setTimeout(load, delay);
    };
    const load = () => {
      api(`api/fs/file?path=${encodeURIComponent(filePath)}`)
        .then((d) => {
          if (!alive) return;
          if (isTransientErr(d)) return retry();
          settled = true;
          if (d && d.error) setErr(d.error.message || tr("view.cannot_load"));
          else setData(d);
        })
        .catch(() => alive && retry());
    };
    const onVis = () => {
      if (!document.hidden && alive && !settled) {
        tries = 0;
        window.clearTimeout(timer);
        load();
      }
    };
    load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
  const markdownLanguage = langFor(filePath) === "markdown";
  const isMarkdown = isText && !huge && markdownLanguage;
  const isMarp = isMarkdown && isMarpDoc(data!.content);
  const editableSnapshotValid = useMemo(() => {
    if (data?.editable !== true || typeof data.content !== "string" || typeof data.revision !== "string") return false;
    try {
      return revisionOf(data.content) === data.revision;
    } catch {
      return false;
    }
  }, [data]);
  const canEdit =
    !!paneId &&
    isText &&
    !markdownLanguage &&
    !isImage &&
    editableSnapshotValid;
  const editorInitial = useMemo(
    () =>
      canEdit
        ? {
            path: data!.path || filePath,
            content: data!.content!,
            revision: data!.revision!,
          }
        : null,
    [canEdit, data, filePath],
  );
  const editor = useFileEditor(paneId || `file:${filePath}`, editorInitial);
  const viewContent = editor.model?.content ?? data?.content ?? "";

  // A line citation always opens Markdown as source so the referenced source row exists.
  // Otherwise a freshly opened Marp deck defaults to slides and other Markdown to preview.
  useEffect(() => {
    if (isText) setMdMode(targetLine ? "source" : isMarp ? "slides" : "preview");
  }, [data, isMarp, isText, targetLine]);

  useEffect(() => {
    setMode("view");
    setEditorNotice("");
    setResolutionOpen(true);
  }, [filePath]);

  useEffect(() => {
    if (mode !== "edit" || !canEdit) return;
    queueMicrotask(() => editorFocusRef.current?.());
  }, [canEdit, mode]);

  const showSlides = isMarp && mdMode === "slides";
  const showPreview = isMarkdown && mdMode === "preview";

  // Expose the preview/source toggle to the keyboard system (viewer.mdMode command).
  // Cycles the available modes: [slides,] preview, source. Registered only for Markdown,
  // so the command no-ops on other files. Keyed by the host pane's id.
  useEffect(() => {
    if (!paneId || !isMarkdown) return;
    const modes: MdMode[] = isMarp ? ["slides", "preview", "source"] : ["preview", "source"];
    return registerPaneViewActions(paneId, {
      toggleMdMode: () =>
        setMdMode((prev) => modes[(modes.indexOf(prev) + 1) % modes.length]),
    });
  }, [paneId, isMarkdown, isMarp]);

  // Highlight once per file load; fall back to escaped plain text. Huge files
  // skip highlighting entirely (plain mode below). Markdown source is deliberately
  // NOT highlighted: its "rendered" view is the preview, so colouring the raw markup
  // adds little, while hljs's markdown grammar emits many <span>s per line — the
  // dominant cost that made a doc-heavy source view slow to open and freeze on a wide
  // text-selection. Plain escaped text keeps line numbers / wrap / selection, cheaply.
  const html = useMemo(() => {
    if (!isText || huge) return "";
    const lang = langFor(filePath);
    try {
      if (lang && lang !== "markdown" && hljs.getLanguage(lang)) {
        return hljs.highlight(viewContent, { language: lang, ignoreIllegals: true }).value;
      }
    } catch {}
    return escapeHtml(viewContent);
  }, [isText, huge, viewContent, filePath]);

  const onEditorValidationError = (error: BufferValidationError) => {
    const messages: Record<BufferValidationError["code"], string> = {
      too_large: tr("editor.validation.too_large"),
      binary_not_supported: tr("editor.validation.binary_not_supported"),
      unsupported_newline: tr("editor.validation.unsupported_newline"),
      invalid_unicode: tr("editor.validation.invalid_unicode"),
    };
    setEditorNotice(messages[error.code]);
  };

  const changeMode = (next: "view" | "edit") => {
    if (next === mode) return;
    setMode(next);
    if (next === "view") queueMicrotask(() => viewTabRef.current?.focus());
  };

  const onModeKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const next =
      event.key === "ArrowLeft" || event.key === "Home"
        ? "view"
        : "edit";
    changeMode(next);
    (next === "view" ? viewTabRef : editTabRef).current?.focus();
  };

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

  // 朗読ビュー（docs/24）を開く。読み上げ＋縦書き閲覧は専用の ReaderView（kind="read"）に集約。
  const openReader = () => openTarget({ content: { kind: "read", filePath } });

  if (!filePath) return <div className="fileview" />;

  const phase = editor.model?.phase;
  const editorStatus =
    editorNotice ||
    (phase === "dirty" && editor.model?.message) ||
    (phase === "saving"
      ? tr("editor.status.saving")
      : phase === "saved"
        ? tr("editor.status.saved")
        : phase === "clean_risk_accepted"
          ? tr("editor.status.risk_accepted")
          : phase === "save_state_unknown"
            ? tr("editor.status.unknown")
            : phase === "conflict"
              ? tr("editor.status.conflict")
              : phase === "conflict_remote_unavailable"
                ? tr("editor.status.remote_unavailable")
                : editor.model?.dirty
                  ? editor.model.riskAccepted
                    ? tr("editor.status.dirty_risk")
                    : tr("editor.status.dirty")
                  : tr("editor.status.clean"));
  const editorAlert =
    phase === "save_state_unknown" ||
    phase === "conflict" ||
    phase === "conflict_remote_unavailable";

  const viewerStyle = {
    "--viewer-font": fontStack(settings.viewerFont),
    "--viewer-size": settings.viewerSize + "px",
    "--viewer-tab": settings.tabSize,
  } as CSSProperties;

  return (
    // Keyboard selection (Shift+arrows) is captured via the debounced `selectionchange`
    // listener below, NOT onKeyUp: firing per keypress made a held Shift+↓ run
    // range.toString() + a state update on every repeat, degrading O(n²) as the selection
    // grew. Mouse selection keeps its instant onMouseUp (one event per drag).
    <div className="fileview" style={viewerStyle} ref={bodyRef} onMouseUp={captureSelection}>
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
          {!isImage && isText ? tr("view.lines_meta", { n: lines }) : ""}
          {data?.truncated ? tr("view.head_only") : ""}
        </span>
        {huge && (
          <span className="fi-tag" title={tr("view.plain_mode_tip")}>
            {tr("view.plain_mode")}
          </span>
        )}
        {data?.lfs && (
          <span className="fi-tag" title={tr("view.lfs_tip")}>
            {tr("view.lfs_pointer")}
          </span>
        )}
        {canEdit && (
          <>
            <div
              className="ui-seg sm file-mode-tabs"
              role="tablist"
              aria-label={tr("editor.mode_group")}
              onKeyDown={onModeKeyDown}
            >
              <button
                ref={viewTabRef}
                type="button"
                className={"seg-btn" + (mode === "view" ? " active" : "")}
                role="tab"
                aria-selected={mode === "view"}
                tabIndex={mode === "view" ? 0 : -1}
                onClick={() => changeMode("view")}
              >
                {tr("editor.mode.view")}
              </button>
              <button
                ref={editTabRef}
                type="button"
                className={"seg-btn" + (mode === "edit" ? " active" : "")}
                role="tab"
                aria-selected={mode === "edit"}
                tabIndex={mode === "edit" ? 0 : -1}
                onClick={() => changeMode("edit")}
              >
                {tr("editor.mode.edit")}
              </button>
            </div>
            <button
              type="button"
              className="file-save-btn"
              disabled={
                !editor.model?.dirty ||
                phase === "saving" ||
                phase === "conflict" ||
                phase === "conflict_remote_unavailable" ||
                phase === "save_state_unknown"
              }
              onClick={() => void editor.save()}
              title={tr("editor.save_tip")}
            >
              <Icon name={phase === "saving" ? "loading" : "save"} spin={phase === "saving"} /> {tr("editor.save")}
            </button>
          </>
        )}
        {isMarkdown && (
          <span className="ui-seg sm md-toggle" role="group" aria-label={tr("view.markdown_display_mode")}>
            {isMarp && (
              <button type="button" aria-pressed={mdMode === "slides"} className={"seg-btn" + (mdMode === "slides" ? " active" : "")} onClick={() => setMdMode("slides")}>
                {tr("view.slides")}
              </button>
            )}
            <button type="button" aria-pressed={mdMode === "preview"} className={"seg-btn" + (mdMode === "preview" ? " active" : "")} onClick={() => setMdMode("preview")}>
              {tr("view.preview")}
            </button>
            <button type="button" aria-pressed={mdMode === "source"} className={"seg-btn" + (mdMode === "source" ? " active" : "")} onClick={() => setMdMode("source")}>
              {tr("view.source")}
            </button>
          </span>
        )}
        {isImage && isText && (
          <span className="ui-seg sm md-toggle" role="group" aria-label={tr("view.image_display_mode")}>
            <button type="button" aria-pressed={imgMode === "preview"} className={"seg-btn" + (imgMode === "preview" ? " active" : "")} onClick={() => setImgMode("preview")}>
              {tr("view.preview")}
            </button>
            <button type="button" aria-pressed={imgMode === "source"} className={"seg-btn" + (imgMode === "source" ? " active" : "")} onClick={() => setImgMode("source")}>
              {tr("view.source")}
            </button>
          </span>
        )}
        {isText && !huge && (
          <span className="ui-seg sm md-toggle">
            <button type="button" className="seg-btn" onClick={openReader} title={tr("view.open_reader_tip")}>
              <Icon name="book" /> {tr("view.read_aloud")}
            </button>
          </span>
        )}
        <span className="fi-path muted" title={filePath}>
          {filePath}
        </span>
        <a className="fi-dl" href={downloadURL(filePath)} download={baseName(filePath)} title={tr("view.download")}>
          <Icon name="cloud-download" />
        </a>
      </header>

      {canEdit && (
        <div
          className={"file-editor-status" + (editorAlert ? " is-alert" : "")}
          role="status"
          aria-live="polite"
        >
          {editorStatus}
        </div>
      )}

      {canEdit && editor.model && (
        <div className="file-editor-shell" hidden={mode !== "edit"}>
          <CodeEditor
            path={editor.model.path}
            content={editor.model.content}
            wrap={wrapOn}
            onChange={editor.edit}
            onSave={() => void editor.save()}
            onValidationError={onEditorValidationError}
            onReady={(focus) => {
              editorFocusRef.current = focus;
              if (mode === "edit") queueMicrotask(focus);
            }}
          />
          {editor.mergeMine && (
            <aside className="file-merge-reference" aria-label={tr("editor.merge_reference_aria")}>
              <strong>{tr("editor.merge_reference")}</strong>
              <pre>{editor.mergeMine}</pre>
            </aside>
          )}
        </div>
      )}

      <div className="file-viewer-shell" hidden={canEdit && mode === "edit"}>
        {err ? (
          <pre className="filebody muted">({err})</pre>
        ) : data == null ? (
          <pre className="filebody muted">…</pre>
        ) : isImage && (!isText || imgMode === "preview") ? (
          <ImageView src={downloadURL(filePath)} alt={baseName(filePath)} onLoad={setImgDims} />
        ) : data.binary ? (
          <pre className="filebody muted">({tr("view.binary")}, {humanSize(data.size)})</pre>
        ) : huge ? (
          <pre className="filebody fb-plain">{viewContent}</pre>
        ) : showSlides ? (
          <MarpView source={viewContent} />
        ) : showPreview ? (
          <div className="md-scroll">
            <MarkdownView source={viewContent} basePath={filePath} onOpenFile={showFile} onOpenDir={revealInFiles} />
          </div>
        ) : (
          <CodeView
            html={html}
            lines={countLines(viewContent)}
            lineNumbers={settings.lineNumbers}
            wrap={wrapOn}
            minimap={settings.minimap}
            marks={marks}
            targetLine={targetLine}
            targetColumn={targetColumn}
          />
        )}
      </div>

      {editor.model && editorAlert && (
        <EditorResolutionPanel
          model={editor.model}
          open={resolutionOpen}
          onOpen={() => setResolutionOpen(true)}
          onCancel={() => setResolutionOpen(false)}
          onRetryConflict={() => void editor.recoverConflict()}
          onRetryUnknown={() => void editor.recoverUnknown()}
          onResave={() => void editor.resaveUnknown()}
          onRiskAccept={editor.riskAccept}
          onTakeRemote={editor.takeRemote}
          onDiscardMine={editor.discardRemote}
          onManualMerge={editor.manualMerge}
          onCopyMine={() => void navigator.clipboard.writeText(editor.model!.content)}
          onClose={() => {
            editor.discard();
            if (paneId) useLayoutStore.getState().closePane(paneId);
          }}
        />
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
              <Icon name="comment-discussion" /> {tr("view.send")}
            </button>
            {/* 音声読み上げが有効なときだけ、選択範囲を読み上げるピルを併置（docs/24）。 */}
            {settings.ttsEnabled && (
              <button
                type="button"
                className="sel-send-pill"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => speakText(sel.quote, tr("view.selection"))}
              >
                <Icon name="unmute" /> {tr("view.read_out")}
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

interface EditorResolutionPanelProps {
  model: FileEditorModel;
  open: boolean;
  onOpen(): void;
  onCancel(): void;
  onRetryConflict(): void;
  onRetryUnknown(): void;
  onResave(): void;
  onRiskAccept(): void;
  onTakeRemote(): void;
  onDiscardMine(): void;
  onManualMerge(): void;
  onCopyMine(): void;
  onClose(): void;
}

function EditorResolutionPanel(props: EditorResolutionPanelProps) {
  const tr = useT();
  const { model } = props;
  if (!props.open) {
    return (
      <div className="file-editor-resolution collapsed" role="alert">
        <button type="button" autoFocus onClick={props.onOpen}>{tr("editor.resolve")}</button>
      </div>
    );
  }
  if (model.phase === "conflict") {
    return (
      <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.conflict")}>
        <h4>{tr("editor.conflict.title")}</h4>
        <p>{tr("editor.conflict.body")}</p>
        {model.conflict && <MineRemoteDiff mine={model.content} remote={model.conflict.content} />}
        <div className="file-editor-actions">
          <button type="button" autoFocus onClick={props.onTakeRemote}>{tr("editor.conflict.adopt_remote")}</button>
          <button type="button" onClick={props.onDiscardMine}>{tr("editor.conflict.discard_mine")}</button>
          <button type="button" onClick={props.onManualMerge}>{tr("editor.conflict.manual_merge")}</button>
          <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
          <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
        </div>
      </section>
    );
  }
  if (model.phase === "conflict_remote_unavailable") {
    return (
      <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.remote_unavailable")}>
        <h4>{tr("editor.status.remote_unavailable")}</h4>
        <p>{tr("editor.remote_unavailable.body")}</p>
        <div className="file-editor-actions">
          <button type="button" autoFocus onClick={props.onRetryConflict}>{tr("editor.retry_get")}</button>
          <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
          <button type="button" onClick={props.onClose}>{tr("editor.close_without_save")}</button>
          <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
        </div>
      </section>
    );
  }
  const observation = model.unknownObservation;
  return (
    <section className="file-editor-resolution" role="alert" aria-label={tr("editor.status.unknown")}>
      <h4>{tr("editor.status.unknown")}</h4>
      <p>
        {observation?.kind === "sent_live"
          ? tr("editor.unknown.sent_live")
          : observation?.kind === "old_base_live"
            ? tr("editor.unknown.old_base")
            : tr("editor.unknown.unavailable")}
      </p>
      <div className="file-editor-actions">
        {observation?.kind === "sent_live" && (
          <>
            <button type="button" autoFocus onClick={props.onResave}>{tr("editor.unknown.resave")}</button>
            <button type="button" onClick={props.onRiskAccept}>{tr("editor.unknown.accept_risk")}</button>
          </>
        )}
        {observation?.kind === "old_base_live" && (
          <button type="button" autoFocus onClick={props.onResave}>{tr("editor.unknown.resave_old")}</button>
        )}
        {(!observation || observation.kind === "unavailable") && (
          <button type="button" autoFocus onClick={props.onRetryUnknown}>{tr("editor.unknown.retry")}</button>
        )}
        <button type="button" onClick={props.onCopyMine}>{tr("editor.copy_mine")}</button>
        <button type="button" onClick={props.onClose}>{tr("editor.close_without_save")}</button>
        <button type="button" onClick={props.onCancel}>{tr("editor.cancel")}</button>
      </div>
    </section>
  );
}

function MineRemoteDiff({ mine, remote }: { mine: string; remote: string }) {
  const tr = useT();
  const mineLines = mine.split("\n");
  const remoteLines = remote.split("\n");
  let prefix = 0;
  while (
    prefix < mineLines.length &&
    prefix < remoteLines.length &&
    mineLines[prefix] === remoteLines[prefix]
  ) prefix++;
  let suffix = 0;
  while (
    suffix < mineLines.length - prefix &&
    suffix < remoteLines.length - prefix &&
    mineLines[mineLines.length - 1 - suffix] === remoteLines[remoteLines.length - 1 - suffix]
  ) suffix++;
  const column = (lines: string[], side: "mine" | "remote") => (
    <div className="file-diff-column">
      <strong>{side}</strong>
      <pre>
        {lines.map((line, index) => {
          const changed = index >= prefix && index < lines.length - suffix;
          return (
            <span key={index} className={changed ? `diff-${side}` : "diff-same"}>
              {changed ? (side === "mine" ? "− " : "+ ") : "  "}{line}{"\n"}
            </span>
          );
        })}
      </pre>
    </div>
  );
  return (
    <div className="file-mine-remote-diff" aria-label={tr("editor.diff_aria")}>
      {column(mineLines, "mine")}
      {column(remoteLines, "remote")}
    </div>
  );
}
