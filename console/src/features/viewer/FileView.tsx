// FileView — a single file (read-only) with CodeLeaf-style affordances: info bar
// (name / language / size / lines / truncation), syntax-highlighted code with a
// gutter + minimap + git change bar, markdown preview/source/slides toggle,
// image preview, and the selection send pill (「送る」, SendSelectionModal). Port of
// views/FileView onto the zustand stores.
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, FocusEvent, KeyboardEvent, ReactNode } from "react";
import { SendSelectionModal } from "../memo/SendSelectionModal.tsx";
import hljs from "highlight.js/lib/common";
import { langFor, countLines, isMarpDoc, imageFormat, isDrawioFile, isPdfFile, documentFormat } from "../../lib/filemeta.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { speakText } from "../chat/tts.ts";
import { DrawioView, type DrawioState } from "./DrawioView.tsx";
import { registerPaneViewActions } from "./paneViewActions.ts";
import { dismissSoftKeyboard, escapeHtml } from "./parts/fileDom.ts";
import { useFileContent, type FileData } from "./parts/useFileContent.ts";
import { useScrollMemory } from "./parts/useScrollMemory.ts";
import { scrollMemoryKey } from "./scrollMemory.ts";
import { useSelectionPill } from "./parts/useSelectionPill.ts";
import {
  editorStatusText,
  externalNoteText,
  isEditorAlert,
  suggestErrText,
  validationErrText,
} from "./parts/fileStatusText.ts";
import {
  FileDiagramControls,
  FileEditControls,
  FileHeadMeta,
  FileHeadPath,
  FileImageModeToggle,
  FileMarkdownControls,
  FileReaderButton,
} from "./parts/FileHeadControls.tsx";
import { EditorResolutionPanel } from "./parts/EditorResolutionPanel.tsx";
import { FileViewerShell } from "./parts/FileViewerShell.tsx";
import { EditorSuggestPanel } from "./parts/EditorSuggestPanel.tsx";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor.tsx";
import { useFileEditor } from "../editor/useFileEditor.ts";
import { useExternalChangeProbe } from "../editor/probe.ts";
import { getEditableFile, type EditableFile, type FileProbeResult } from "../editor/api.ts";
import { useDebounced } from "../../lib/useDebounced.ts";
import { revisionOf, type BufferValidationError } from "../editor/buffer.ts";
import {
  cycleFileMode,
  initialFileMode,
  paneModeOf,
  reconcileFileMode,
  rendererControls,
  surfaceKey,
  surfacesFor,
  withDiagramMode,
  withMarkdownMode,
  withPaneMode,
  withRenderer,
  type DiagramMode,
  type FileModeCaps,
  type FileModeState,
  type MarkdownMode,
  type PreviewRenderer,
} from "./fileMode.ts";

/** How long the Markdown preview waits after typing stops. Long enough that a
 *  burst of keystrokes renders once, short enough to feel live. */
const PREVIEW_DEBOUNCE_MS = 200;

interface FileViewProps {
  filePath: string;
  targetLine?: number;
  targetColumn?: number;
  wrap?: boolean | null;
  /** The starting display mode the opener asked for ("編集で開く" from a file
   * menu). Only the FIRST mode is taken from it — the pane's own controls own
   * the mode from there on. */
  openMode?: "view" | "edit";
  /** The host pane's id — lets global keyboard commands drive this view's local
   * Markdown preview/source toggle via the pane-view action registry. */
  paneId?: string;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
}

export function FileView({ filePath, targetLine, targetColumn, wrap, openMode, paneId, headerActions }: FileViewProps) {
  const tr = useT();
  const openTarget = useLayoutStore((s) => s.openTarget);
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setActivePane = useLayoutStore((s) => s.setActive);
  const isActivePane = useLayoutStore((s) => s.layout.cols.some((col) => col.cells.some((cell) => cell.id === s.layout.activeCellId && cell.selectedViewId === paneId)));
  const revealInFiles = useFilesStore((s) => s.revealInFiles);
  const settings = useSettings();
  // wrap is the per-pane override; fall back to the global setting.
  const wrapOn = wrap === undefined || wrap === null ? settings.wrap : wrap;
  // Content plus the display state that resets with it on a reopen (parts/useFileContent).
  // The two effects that run first in this view (linemarks / loading) live there too.
  const {
    data,
    setData,
    dataRef,
    err,
    viewNotice,
    setViewNotice,
    imgMode,
    setImgMode,
    imgDims,
    setImgDims,
    pdfPages,
    setPdfPages,
    marks,
  } = useFileContent(filePath);
  // Scroll memory. Switching tabs and coming back unmounts this view entirely and refetches the
  // content (PaneHost draws only the selected view), so the reading position is kept outside the
  // component. One memory per view.
  const scrollMemory = useScrollMemory(scrollMemoryKey(paneId, filePath));
  // State reported back by the diagram view (page count, zoom); used only by the header.
  const [diagramState, setDiagramState] = useState<DrawioState | null>(null);
  // Once shown, the diagram view is never unmounted, only hidden: rebuilding it reloads the 4 MB
  // viewer and loses the zoom and the open page. It is not created before it is first shown
  // either, so someone who only reads the source never pays for fetching the diagram.
  const [diagramMounted, setDiagramMounted] = useState(false);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [sendOpen, setSendOpen] = useState(false);
  // Keyed by the loaded file so a new document re-picks its starting mode. The
  // stored mode is the user's intent; what renders is derived from it below.
  const [modeState, setModeState] = useState<{
    key: FileData | null;
    targetLine: number | undefined;
    openMode: "view" | "edit" | undefined;
    mode: FileModeState;
  }>({ key: null, targetLine: undefined, openMode: undefined, mode: { kind: "plain", mode: "view" } });
  // Bumped by each mode selection that lands on the editing surface; the effect
  // below turns one bump into one focus move (docs/log/44 §5).
  const [focusRequest, setFocusRequest] = useState(0);
  const focusRequestRef = useRef(0);
  const consumedFocusRef = useRef(0);
  const [editorNotice, setEditorNotice] = useState("");
  const [resolutionOpen, setResolutionOpen] = useState(true);
  const viewTabRef = useRef<HTMLButtonElement>(null);
  const editTabRef = useRef<HTMLButtonElement>(null);
  const editorFocusRef = useRef<(() => void) | null>(null);
  const editorHandleRef = useRef<CodeEditorHandle | null>(null);
  const modeGroupRef = useRef<HTMLSpanElement>(null);
  // AI suggestions (docs/log/44 Phase 4): the compose panel's open state and instruction text.
  // The review stage is held by the presence of editor.model.suggestion, not here.
  const [suggestOpen, setSuggestOpen] = useState(false);
  const [suggestInstruction, setSuggestInstruction] = useState("");

  const showFile = (path: string, line?: number, column?: number, openInNew = false) => {
    const target = { content: { kind: "file" as const, filePath: path, targetLine: line, targetColumn: column } };
    if (openInNew) openTargetInNew(target, true);
    else openTarget(target);
  };

  const imgFmt = imageFormat(filePath);
  const isImage = !!imgFmt;
  const isPdf = isPdfFile(filePath);
  // Word / Excel / PowerPoint … (docs/log/82 §82.4). Unlike PDF these are not rendered as-is but
  // converted to Markdown to read, so the format is tracked separately.
  const docFmt = documentFormat(filePath);
  const isDoc = !!docFmt;

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
  const editableSnapshotValid = useMemo(() => {
    if (data?.editable !== true || typeof data.content !== "string" || typeof data.revision !== "string") return false;
    try {
      return revisionOf(data.content) === data.revision;
    } catch {
      return false;
    }
  }, [data]);
  // Markdown in the viewer's plain fallback offers neither control group: the
  // three-mode group needs a preview the fallback cannot render, and the
  // non-Markdown tablist must not appear on a Markdown file (docs/log/44 §1.1). It
  // therefore stays read-only, pinned to view. Other huge text files keep their
  // editing surface — CodeMirror virtualises rows, so only the read-only grid
  // needed the fallback in the first place.
  const plainModeMarkdown = huge && markdownLanguage;
  const canEdit = !!paneId && isText && !isImage && !plainModeMarkdown && editableSnapshotValid;
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

  // --- Following external changes (docs/log/44 §7, Phase 3.5) ------------------
  // Buffered panes route probe observations through the editor model; a pane
  // showing an editable file without a buffer (the plain-mode Markdown
  // fallback) follows by replacing the loaded data directly.
  const syncFollowedData = (file: EditableFile) => {
    const prev = dataRef.current;
    if (!prev || prev.editable !== true) return;
    const next: FileData = { ...prev, path: file.path, content: file.content, size: file.size, revision: file.revision };
    setData(next);
    // A follow is the same document with newer bytes, not a new file: carry the
    // mode state over to the new data object so the user's chosen mode holds.
    setModeState((m) => (m.key === prev ? { ...m, key: next } : m));
  };

  const applyViewProbe = async (result: FileProbeResult) => {
    const current = dataRef.current;
    if (!current || current.editable !== true || typeof current.revision !== "string") return;
    if (result.kind === "revision") {
      if (result.revision === current.revision) {
        setViewNotice("");
        return;
      }
      try {
        const file = await getEditableFile(current.path || filePath);
        if (dataRef.current !== current || file.revision === current.revision) return;
        syncFollowedData(file);
        setViewNotice(tr("editor.external.followed"));
      } catch {
        // Silent (docs/log/44 §7.5) — the next trigger retries.
      }
      return;
    }
    setViewNotice(
      result.kind === "missing"
        ? tr("editor.external.missing")
        : result.kind === "uneditable"
          ? tr("editor.external.uneditable")
          : tr("editor.external.boundary"),
    );
  };

  const probePath =
    canEdit && editor.model
      ? editor.model.path
      : data?.editable === true && typeof data.revision === "string" && isText && !isImage
        ? data.path || filePath
        : null;

  useExternalChangeProbe({
    path: probePath,
    paneActive: !!isActivePane,
    isPaneVisible: () => {
      // A phone keeps background columns mounted but display:none (PaneHost).
      const el = bodyRef.current;
      if (!el) return false;
      return typeof el.checkVisibility === "function" ? el.checkVisibility() : el.offsetWidth > 0;
    },
    isSaving: () => editor.model?.phase === "saving",
    onResult: (result) => {
      if (editor.model) {
        void editor.applyProbeResult(result).then((file) => {
          if (file) syncFollowedData(file);
        });
      } else {
        void applyViewProbe(result);
      }
    },
  });
  // The preview renders the buffer, so it would otherwise re-parse + sanitise +
  // re-run Mermaid on every keystroke. Debouncing also settles the Marp check:
  // a deck's front matter is edited character by character, and reacting to each
  // intermediate state would make the renderer buttons appear and disappear
  // while typing (docs/log/44 §1.1).
  //
  // The delay applies to editing only. Keying on the loaded file — not its path,
  // which does not change when a GET lands — seeds the freshly read content at
  // once. Otherwise the initial Marp check would run on an empty preview source
  // and open a deck in the normal preview, which reconcile then keeps.
  const previewSource = useDebounced(viewContent, PREVIEW_DEBOUNCE_MS, data);
  const isMarp = isMarkdown && isMarpDoc(previewSource);

  // What the loaded file allows. The mode state machine (docs/log/44 §1.1) derives
  // the pane's view/edit layer and the surfaces to render from these.
  // Whether it can open as a diagram. .drawio / .dio are decided by extension, .xml by the head
  // of its content (docs/log/65 §65.4). Extension-based detection still holds when content was
  // truncated past 2 MiB: the diagram itself is refetched via download, so it still opens.
  const isDiagram = useMemo(
    () => isDrawioFile(filePath, isText ? (data?.content ?? "").slice(0, 4096) : null),
    [filePath, isText, data],
  );
  const caps = useMemo<FileModeCaps>(
    () => ({ markdown: isMarkdown, marp: isMarp, editable: canEdit, diagram: isDiagram }),
    [canEdit, isMarkdown, isMarp, isDiagram],
  );
  const capsRef = useRef(caps);
  capsRef.current = caps;

  // A freshly loaded file picks its own starting mode: a line citation opens the
  // source surface so the cited row exists, a Marp deck opens as slides, other
  // Markdown as a preview. The choice is made on the render the file lands —
  // deferring it to an effect would let one frame paint the other control group,
  // since capabilities become known before the state that reads them.
  // A new citation re-picks it too: retargeting the pane at a line of the file it
  // already shows leaves `data` untouched, and staying in the preview would hide
  // the very row that was asked for (docs/log/44 §1.8).
  // A fresh open request re-picks it too, the same way a new citation does:
  // choosing edit (「編集」) for the file a pane already shows retargets that pane (same
  // `data`), and only the request is new.
  const startingMode = () => initialFileMode(caps, { hasTargetLine: !!targetLine, requested: openMode });
  const modeIsCurrent = modeState.key === data && modeState.targetLine === targetLine && modeState.openMode === openMode;
  if (data && !modeIsCurrent) setModeState({ key: data, targetLine, openMode, mode: startingMode() });
  // Capability changes after that only clamp the selection, so editing a deck's
  // front matter cannot yank the user back to the initial mode.
  const fileMode = modeIsCurrent ? reconcileFileMode(modeState.mode, caps) : startingMode();
  const paneMode = paneModeOf(fileMode, caps);
  const surfaces = surfacesFor(fileMode, caps);
  const renderers = rendererControls(fileMode, caps);
  // The keyboard command's handler is registered once per file, so it must read
  // the current mode through a ref rather than the render it was created in.
  const fileModeRef = useRef(fileMode);
  fileModeRef.current = fileMode;

  // Focus follows the mode, but only when the user asked for the mode (docs/log/44
  // §5). Opening a file — a citation lands in the source surface — must not pull
  // focus out of whatever the user was typing in.
  const updateMode = (next: (prev: FileModeState) => FileModeState) => {
    const current = fileModeRef.current;
    const target = next(current);
    const currentSurfaces = surfacesFor(current, capsRef.current);
    const nextSurfaces = surfacesFor(target, capsRef.current);
    const editorEl = bodyRef.current?.querySelector(".file-editor-cm");
    const leavingFocusedEditor =
      !nextSurfaces.editor && !!editorEl && editorEl.contains(document.activeElement);
    setModeState((prev) => ({ ...prev, mode: target }));
    if (nextSurfaces.editor && surfaceKey(nextSurfaces) !== surfaceKey(currentSurfaces)) {
      // The move between surfaces is the unit of focus, not the surface merely
      // being on screen: split and edit both show the editor, so a switch
      // between them would never re-run a visibility-driven effect. Counting the
      // requests also makes a superseded one harmless — the effect reads the
      // surface as it ends up, so a request the next selection cancelled just
      // finds nothing to focus.
      //
      // Picking a preview renderer changes no surface, so it leaves focus on the
      // renderer group the user is working in (docs/log/44 §5).
      setFocusRequest((n) => n + 1);
    } else {
      // Every other selection — picking a preview renderer, re-picking the mode
      // already shown — is the user working somewhere else in the header. A
      // request still waiting for the editor to come up belongs to an earlier
      // selection, so it must not outlive this one and pull focus out of the
      // group being operated.
      consumedFocusRef.current = focusRequestRef.current;
      if (leavingFocusedEditor) {
        // The keyboard command can hide the surface that holds focus; hand it to
        // the control that describes where we landed instead of dropping it.
        queueMicrotask(() =>
          modeGroupRef.current?.querySelector<HTMLButtonElement>('[aria-pressed="true"]')?.focus(),
        );
      }
    }
  };

  useEffect(() => {
    if (surfaces.diagram) setDiagramMounted(true);
  }, [surfaces.diagram]);

  useEffect(() => {
    setEditorNotice("");
    setResolutionOpen(true);
    setSuggestOpen(false);
    setSuggestInstruction("");
  }, [filePath]);

  focusRequestRef.current = focusRequest;

  // Leaving the editing surface retires every request still outstanding. A
  // request only survives its own selection: without this, one made while the
  // editor was still mounting would sit there until the surface came back for
  // some other reason — a citation, a new file — and steal focus from whatever
  // the user had moved on to.
  useEffect(() => {
    if (surfaces.editor) return;
    consumedFocusRef.current = focusRequestRef.current;
  }, [surfaces.editor]);

  useEffect(() => {
    // A new file is a new pane as far as focus is concerned; a request aimed at
    // the previous one must not follow it here.
    consumedFocusRef.current = focusRequestRef.current;
  }, [filePath]);

  // Deliberately keyed on the request, not on the surface: the same request id
  // must fire once per user selection even when the surface was already there.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!focusRequest) return;
    if (!surfaces.editor) {
      // The request was raised for a surface this commit does not show, so the
      // selection behind it has already been superseded — by another selection
      // in the same batch, or by a clamp. Nothing is waiting for it. Retiring it
      // here as well as on the surface disappearing covers the case where the
      // surface never appeared at all, so its disappearance is never observed.
      consumedFocusRef.current = focusRequest;
      return;
    }
    queueMicrotask(() => {
      // CodeMirror may not have mounted yet — the pane's controls appear a commit
      // before the editor does. Leave the request unconsumed and let onReady take
      // it, rather than dropping the focus move on a slow start or a fast click.
      if (consumedFocusRef.current === focusRequest || !editorFocusRef.current) return;
      consumedFocusRef.current = focusRequest;
      editorFocusRef.current();
    });
  }, [focusRequest]);

  // Opening or returning to a file view from a composer/terminal must also
  // retract Gboard.  This runs only for the active file pane and deliberately
  // leaves edit mode alone, where a keyboard is needed for CodeMirror.
  useEffect(() => {
    if (paneMode === "view" && isActivePane && coarsePointer()) dismissSoftKeyboard();
  }, [filePath, isActivePane, paneMode]);

  // A rejected transaction gets an immediate validation announcement. Once a
  // valid edit or state transition follows, return the live region to the normal
  // dirty/saving/saved/recovery status instead of letting the old error mask it.
  useEffect(() => {
    setEditorNotice("");
  }, [editor.model?.bufferGeneration, editor.model?.phase]);

  // Announce a clean auto-follow (docs/log/44 §7.4). Declared after the clearing
  // effect so the same commit's clear cannot mask the announcement; the next
  // buffer change then clears it through the effect above.
  const followEpoch = editor.model?.followEpoch ?? 0;
  const followEpochRef = useRef(followEpoch);
  useEffect(() => {
    const was = followEpochRef.current;
    followEpochRef.current = followEpoch;
    if (followEpoch > was) setEditorNotice(tr("editor.external.followed"));
  }, [followEpoch, tr]);

  // Expose the Markdown mode walk to the keyboard system (viewer.mdMode command).
  // Modes are the outer axis and the preview renderer the inner one, so a Marp
  // deck reaches both of its previews without a fourth top-level mode (§1.1).
  // Registered only for Markdown, so the command no-ops on other files.
  useEffect(() => {
    if (!paneId || (!isMarkdown && !isDiagram)) return;
    return registerPaneViewActions(paneId, {
      toggleMdMode: () => updateMode((prev) => cycleFileMode(prev, capsRef.current)),
    });
  }, [paneId, isMarkdown, isDiagram]);

  // Highlight once per file load; fall back to escaped plain text. Huge files
  // skip highlighting entirely (plain mode below). Markdown source is deliberately
  // NOT highlighted: its "rendered" view is the preview, so colouring the raw markup
  // adds little, while hljs's markdown grammar emits many <span>s per line — the
  // dominant cost that made a doc-heavy source view slow to open and freeze on a wide
  // text-selection. Plain escaped text keeps line numbers / wrap / selection, cheaply.
  // Recomputed only while the read-only CodeView is actually on screen
  // (surfaces.source — its render branch's condition). While the editor or a
  // preview is up the hidden shell still renders the CodeView as a child, and
  // keying on the edit buffer would re-run the full-document hljs pass on every
  // keystroke; keep the last value instead. Switching back to the source
  // surface flips the dep, so a fresh highlight replaces the stale one.
  const htmlRef = useRef("");
  const html = useMemo(() => {
    if (!isText || huge) return "";
    if (!surfaces.source) return htmlRef.current;
    const lang = langFor(filePath);
    try {
      if (lang && lang !== "markdown" && hljs.getLanguage(lang)) {
        return hljs.highlight(viewContent, { language: lang, ignoreIllegals: true }).value;
      }
    } catch {}
    return escapeHtml(viewContent);
  }, [isText, huge, viewContent, filePath, surfaces.source]);
  htmlRef.current = html;

  const onEditorValidationError = (error: BufferValidationError) => {
    setEditorNotice(validationErrText(tr, error.code));
  };

  // Requests a suggestion for the selection plus the instruction. An empty selection (a bare
  // caret) targets the whole document.
  const submitSuggestion = () => {
    const model = editor.model;
    if (!model) return;
    let range = editorHandleRef.current?.selection() ?? { from: 0, to: model.content.length };
    if (range.from === range.to) range = { from: 0, to: model.content.length };
    setEditorNotice("");
    void editor.requestSuggestion(suggestInstruction, range).then((code) => {
      if (code) setEditorNotice(suggestErrText(tr, code));
      else setSuggestInstruction("");
    });
  };

  const acceptSuggestionIntoView = () => {
    setEditorNotice("");
    const code = editor.acceptSuggestion(
      editorHandleRef.current ? (edit) => editorHandleRef.current!.applyEdit(edit) : undefined,
    );
    if (code) setEditorNotice(suggestErrText(tr, code));
    else setSuggestOpen(false);
  };

  const changeMode = (next: "view" | "edit") => {
    if (next === paneMode) return;
    updateMode((prev) => withPaneMode(prev, next, capsRef.current));
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

  const changeDiagramMode = (next: DiagramMode) => {
    updateMode((prev) => withDiagramMode(prev, next, capsRef.current));
  };

  const changeMarkdownMode = (next: MarkdownMode) => {
    updateMode((prev) => withMarkdownMode(prev, next, capsRef.current));
  };

  const changeRenderer = (next: PreviewRenderer) => {
    updateMode((prev) => withRenderer(prev, next, capsRef.current));
  };

  const onFocusCapture = (event: FocusEvent<HTMLDivElement>) => {
    // A focus move (Tab, programmatic reader focus, or a click) is just as much
    // an activation as pane mouse-down.  Keeping layout.activeId in sync makes
    // ProjectFiles highlight this exact file in the left rail.
    if (paneId) setActivePane(paneId);
    if (paneMode === "view" && coarsePointer()) {
      // In particular, prevent CodeView's read-only contentEditable from being
      // interpreted as an editing field by Android/Gboard.
      const target = event.target;
      if (target instanceof HTMLElement) target.blur();
      const virtualKeyboard = (navigator as Navigator & { virtualKeyboard?: { hide?(): void } }).virtualKeyboard;
      virtualKeyboard?.hide?.();
    }
  };

  // The pill floating over the selection (parts/useSelectionPill). The two capture entry points
  // and the two effects that dismiss it live there.
  const { sel, setSel, captureSelection, captureEditorSelection } = useSelectionPill({
    bodyRef,
    sendOpen,
    editorSurface: surfaces.editor,
  });

  // Opens the reader view (docs/log/24). Speech and vertical-writing reading live entirely in
  // the dedicated ReaderView (kind="read").
  const openReader = () => openTarget({ content: { kind: "read", filePath } });

  if (!filePath) return <div className="fileview" />;

  const phase = editor.model?.phase;
  const editorStatus = editorStatusText(tr, editor.model, editorNotice);
  const editorAlert = isEditorAlert(phase);
  const externalObs = !editorAlert ? (editor.model?.externalObservation ?? null) : null;
  const externalNote = externalNoteText(tr, externalObs);

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
    <div className="fileview" style={viewerStyle} ref={bodyRef} onFocusCapture={onFocusCapture} onMouseUp={captureSelection}>
      <ViewHead className="fileinfo" actions={headerActions}>
        <FileHeadMeta
          filePath={filePath}
          size={data?.size}
          truncated={data?.truncated}
          lfs={data?.lfs}
          imgFmt={imgFmt}
          isPdf={isPdf}
          isDoc={isDoc}
          isText={isText}
          imgDims={imgDims}
          pdfPages={pdfPages}
          lines={lines}
          huge={huge}
        />
        {canEdit && (
          <FileEditControls
            showTabs={fileMode.kind === "plain"}
            paneMode={paneMode}
            viewTabRef={viewTabRef}
            editTabRef={editTabRef}
            onModeKeyDown={onModeKeyDown}
            onChangeMode={changeMode}
            saveDisabled={
              !editor.model?.dirty ||
              phase === "saving" ||
              phase === "conflict" ||
              phase === "conflict_remote_unavailable" ||
              phase === "save_state_unknown"
            }
            saving={phase === "saving"}
            suggestDisabled={editorAlert || phase === "saving" || editor.suggesting}
            suggestEnabled={settings.editSuggestEnabled}
            suggesting={editor.suggesting}
            onSave={() => {
              setEditorNotice("");
              void editor.save();
            }}
            onSuggest={() => setSuggestOpen(true)}
          />
        )}
        {fileMode.kind === "diagram" && (
          <FileDiagramControls
            fileMode={fileMode}
            caps={caps}
            modeGroupRef={modeGroupRef}
            onChangeMode={changeDiagramMode}
            showState={surfaces.diagram}
            diagramState={diagramState}
          />
        )}
        {fileMode.kind === "markdown" && (
          <FileMarkdownControls
            fileMode={fileMode}
            caps={caps}
            modeGroupRef={modeGroupRef}
            onChangeMode={changeMarkdownMode}
            renderers={renderers}
            onChangeRenderer={changeRenderer}
          />
        )}
        {isImage && isText && <FileImageModeToggle mode={imgMode} onChange={setImgMode} />}
        {/* No reader button on a diagram: there is no prose to read out (only the mxfile XML),
            so pressing it could do nothing useful. No capability, no control. */}
        {isText && !huge && !isDiagram && <FileReaderButton onOpen={openReader} />}
        <FileHeadPath filePath={filePath} />
      </ViewHead>

      {canEdit && (
        <div
          className={"file-editor-status" + (editorAlert ? " is-alert" : "")}
          role="status"
          aria-live="polite"
        >
          {editorStatus}
          {externalNote && (
            <span className="file-external-note">
              {externalNote}
              {externalObs?.kind === "changed" && phase === "dirty" && (
                <button
                  type="button"
                  onClick={() => {
                    setEditorNotice("");
                    void editor.confirmExternalChange();
                  }}
                >
                  {tr("editor.external.check_diff")}
                </button>
              )}
            </span>
          )}
        </div>
      )}
      {!canEdit && viewNotice && (
        <div className="file-editor-status" role="status" aria-live="polite">
          {viewNotice}
        </div>
      )}

      <div className={"file-surfaces" + (surfaces.split ? " is-split" : "")}>
        {canEdit && editor.model && (
          <div className="file-editor-shell" hidden={!surfaces.editor}>
            <CodeEditor
              path={editor.model.path}
              content={editor.model.content}
              wrap={wrapOn}
              targetLine={targetLine}
              externalEpoch={editor.model.followEpoch}
              onSelectionChange={captureEditorSelection}
              onChange={(content) => {
                setEditorNotice("");
                editor.edit(content);
              }}
              onSave={() => {
                setEditorNotice("");
                void editor.save();
              }}
              onValidationError={onEditorValidationError}
              onHandle={(handle) => {
                editorHandleRef.current = handle;
              }}
              onReady={(focus) => {
                // Focus is granted by updateMode, never by the surface merely
                // appearing: the editor mounts as soon as the file is editable,
                // which is often before the user has asked to edit anything.
                editorFocusRef.current = focus;
                // This prop is rebuilt every render, so the request and surfaces
                // read here are the current ones: a request made while the editor
                // was still mounting is honoured, one the user has since left
                // behind is not.
                if (focusRequest && surfaces.editor && consumedFocusRef.current !== focusRequest) {
                  consumedFocusRef.current = focusRequest;
                  focus();
                }
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

        {diagramMounted && (
          <div className="file-diagram-shell" hidden={!surfaces.diagram}>
            <DrawioView
              filePath={filePath}
              dark={settings.theme !== "light"}
              onState={setDiagramState}
              onShowSource={() => changeDiagramMode("source")}
            />
          </div>
        )}

        <FileViewerShell
          hidden={!surfaces.source && !surfaces.preview}
          filePath={filePath}
          err={err}
          loaded={data != null}
          size={data?.size}
          binary={data?.binary}
          isImage={isImage}
          isText={isText}
          imgMode={imgMode}
          onImgDims={setImgDims}
          isPdf={isPdf}
          onPdfMeta={(m) => setPdfPages(m.pages)}
          isDoc={isDoc}
          docFmt={docFmt}
          onOpenFile={showFile}
          onOpenDir={(path) => revealInFiles(path, { focus: true })}
          huge={huge}
          viewContent={viewContent}
          preview={surfaces.preview}
          previewSource={previewSource}
          html={html}
          lineNumbers={settings.lineNumbers}
          wrap={wrapOn}
          minimap={settings.minimap}
          marks={marks}
          targetLine={targetLine}
          targetColumn={targetColumn}
          scrollMemory={scrollMemory}
        />
      </div>

      {editor.model && editorAlert && (
        <EditorResolutionPanel
          model={editor.model}
          open={resolutionOpen}
          onOpen={() => setResolutionOpen(true)}
          onCancel={() => setResolutionOpen(false)}
          onRetryConflict={() => {
            setEditorNotice("");
            void editor.recoverConflict();
          }}
          onRetryUnknown={() => {
            setEditorNotice("");
            void editor.recoverUnknown();
          }}
          onResave={() => {
            setEditorNotice("");
            void editor.resaveUnknown();
          }}
          onRiskAccept={() => {
            setEditorNotice("");
            editor.riskAccept();
          }}
          onTakeRemote={() => {
            setEditorNotice("");
            editor.takeRemote();
          }}
          onDiscardMine={() => {
            setEditorNotice("");
            editor.discardRemote();
          }}
          onManualMerge={() => {
            setEditorNotice("");
            editor.manualMerge();
          }}
          onCopyMine={() => void navigator.clipboard.writeText(editor.model!.content)}
          onClose={() => {
            if (paneId) useLayoutStore.getState().closePane(paneId);
          }}
        />
      )}

      {/* AI suggestions (docs/log/44 Phase 4). Yields while a conflict or other alert panel is
          up, the same precedence as the probe advisory. The review stage is held by
          model.suggestion and staleness is derived from the revision. */}
      {canEdit && editor.model && !editorAlert && (suggestOpen || editor.suggesting || editor.model.suggestion) && (
        <EditorSuggestPanel
          model={editor.model}
          suggestion={editor.model.suggestion}
          suggesting={editor.suggesting}
          instruction={suggestInstruction}
          onInstructionChange={setSuggestInstruction}
          onSubmit={submitSuggestion}
          onAccept={acceptSuggestionIntoView}
          onReject={() => {
            setEditorNotice("");
            editor.rejectSuggestion();
            setSuggestOpen(true);
          }}
          onClose={() => {
            editor.cancelSuggestion();
            editor.rejectSuggestion();
            setSuggestOpen(false);
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
            {/* Only when speech is enabled, a second pill reads the selection out (docs/log/24). */}
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
