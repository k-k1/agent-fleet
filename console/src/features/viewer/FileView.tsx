// FileView — a single file (read-only) with CodeLeaf-style affordances: info bar
// (name / language / size / lines / truncation), syntax-highlighted code with a
// gutter + minimap + git change bar, markdown preview/source/slides toggle,
// image preview, and the selection → 送る pill (SendSelectionModal). Port of
// views/FileView onto the zustand stores.
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, FocusEvent, KeyboardEvent, ReactNode } from "react";
import { SendSelectionModal } from "../memo/SendSelectionModal.tsx";
import hljs from "highlight.js/lib/common";
import { api, downloadURL, isTransientErr } from "../../core/api/client.ts";
import { baseName, langFor, langLabel, humanSize, countLines, isMarpDoc, imageFormat, isDrawioFile } from "../../lib/filemeta.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { useSettings, fontStack } from "../../lib/settings.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useFilesStore } from "../files/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { speakText } from "../chat/tts.ts";
import { MarkdownView } from "./MarkdownView.tsx";
import { MarpView } from "./MarpView.tsx";
import { CodeView } from "./CodeView.tsx";
import { ImageView } from "./ImageView.tsx";
import { DrawioView, type DrawioState } from "./DrawioView.tsx";
import { registerPaneViewActions } from "./paneViewActions.ts";
import type { LineMarks } from "./CodeView.tsx";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor.tsx";
import { lineDiff } from "./DiffView.tsx";
import type { EditorSelectionReport } from "../editor/selection.ts";
import type { EditSuggestionEnvelope } from "../editor/suggest.ts";
import { editorPill, type SelectionPill } from "./selectionPill.ts";
import { useFileEditor } from "../editor/useFileEditor.ts";
import { useExternalChangeProbe } from "../editor/probe.ts";
import { getEditableFile, type EditableFile, type FileProbeResult } from "../editor/api.ts";
import { useDebounced } from "../../lib/useDebounced.ts";
import { revisionOf, type BufferValidationError } from "../editor/buffer.ts";
import type { FileEditorModel } from "../editor/model.ts";
import {
  cycleFileMode,
  diagramModeControls,
  initialFileMode,
  markdownModeControls,
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

// A read-only file can use contentEditable for caret browsing, but that makes
// Android treat it as a text field and summon Gboard.  Reading on a touch device
// should leave the screen for the file, not the keyboard.
function dismissSoftKeyboard(): void {
  const focused = document.activeElement;
  if (focused instanceof HTMLElement) focused.blur();
  const virtualKeyboard = (navigator as Navigator & { virtualKeyboard?: { hide?(): void } }).virtualKeyboard;
  virtualKeyboard?.hide?.();
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
  const [data, setData] = useState<FileData | null>(null);
  const dataRef = useRef<FileData | null>(null);
  dataRef.current = data;
  // External-change notice for panes without an editor buffer (docs/44 §7.4's
  // read-only view case); buffered panes speak through the editor status line.
  const [viewNotice, setViewNotice] = useState("");
  const [err, setErr] = useState("");
  const [imgMode, setImgMode] = useState<"preview" | "source">("preview");
  const [imgDims, setImgDims] = useState<{ w: number; h: number } | null>(null);
  // 図の面が返す状態（ページ数・倍率）。ヘッダの表示にだけ使う。
  const [diagramState, setDiagramState] = useState<DrawioState | null>(null);
  // 図の面は **一度出したら畳んでも外さない**（hidden にするだけ）。作り直すと 4MB の
  // ビューアを読み直し、ズーム位置と開いていたページも失う。逆に一度も見ていない
  // うちは作らない —— ソースだけ見て閉じる人に図の取得をさせない。
  const [diagramMounted, setDiagramMounted] = useState(false);
  const [marks, setMarks] = useState<LineMarks | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  // `origin` keeps the two capture paths (the read-only grid's DOM walk and the
  // editor's own report) from clearing each other's pill (docs/44 §1.8).
  const [sel, setSel] = useState<SelectionPill | null>(null);
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
  // below turns one bump into one focus move (docs/44 §5).
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
  // AI 提案（docs/44 Phase 4）: compose パネルの開閉と指示文。レビュー段階は
  // editor.model.suggestion の有無が持つので、ここには置かない。
  const [suggestOpen, setSuggestOpen] = useState(false);
  const [suggestInstruction, setSuggestInstruction] = useState("");

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
    setViewNotice("");
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
  // non-Markdown tablist must not appear on a Markdown file (docs/44 §1.1). It
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

  // --- 外部変更の追従 (docs/44 §7, Phase 3.5) ------------------------------
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
        // Silent (docs/44 §7.5) — the next trigger retries.
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
  // while typing (docs/44 §1.1).
  //
  // The delay applies to editing only. Keying on the loaded file — not its path,
  // which does not change when a GET lands — seeds the freshly read content at
  // once. Otherwise the initial Marp check would run on an empty preview source
  // and open a deck in the normal preview, which reconcile then keeps.
  const previewSource = useDebounced(viewContent, PREVIEW_DEBOUNCE_MS, data);
  const isMarp = isMarkdown && isMarpDoc(previewSource);

  // What the loaded file allows. The mode state machine (docs/44 §1.1) derives
  // the pane's view/edit layer and the surfaces to render from these.
  // 図として開けるか。.drawio / .dio は拡張子で決まり、.xml は中身の頭で決まる
  // （docs/65 §65.4）。2 MiB 超で content が打ち切られていても、拡張子で決まる分は
  // そのまま効く —— 図そのものは download 経由で取り直すので開ける。
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
  // the very row that was asked for (docs/44 §1.8).
  // A fresh open request re-picks it too, the same way a new citation does:
  // choosing 編集 for the file a pane already shows retargets that pane (same
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

  // Focus follows the mode, but only when the user asked for the mode (docs/44
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
      // renderer group the user is working in (docs/44 §5).
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

  // Announce a clean auto-follow (docs/44 §7.4). Declared after the clearing
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
    const messages: Record<BufferValidationError["code"], string> = {
      too_large: tr("editor.validation.too_large"),
      binary_not_supported: tr("editor.validation.binary_not_supported"),
      unsupported_newline: tr("editor.validation.unsupported_newline"),
      invalid_unicode: tr("editor.validation.invalid_unicode"),
    };
    setEditorNotice(messages[error.code]);
  };

  // AI 提案の失敗/棄却コード → 表示文言（docs/44 §3.4: `suggestion_stale` は
  // HTTP ではなく Console 側の安定 UI code）。buffer validator のコードは既存の
  // editor.validation.* を再利用する。
  const suggestErrText = (code: string): string => {
    switch (code) {
      case "suggestion_stale":
        return tr("editor.suggestion.stale");
      case "suggestion_invalid":
        return tr("editor.suggestion.invalid");
      case "selection_too_large":
        return tr("editor.suggestion.selection_too_large");
      case "instruction_invalid":
        return tr("editor.suggestion.instruction_invalid");
      case "io_timeout":
        return tr("editor.suggestion.timeout");
      case "too_large":
      case "binary_not_supported":
      case "unsupported_newline":
      case "invalid_unicode":
        return tr(`editor.validation.${code}`);
      default:
        return tr("editor.suggestion.failed");
    }
  };

  // 選択範囲＋指示文で生成を依頼する。選択が空（カーソルのみ）なら全文を対象にする。
  const submitSuggestion = () => {
    const model = editor.model;
    if (!model) return;
    let range = editorHandleRef.current?.selection() ?? { from: 0, to: model.content.length };
    if (range.from === range.to) range = { from: 0, to: model.content.length };
    setEditorNotice("");
    void editor.requestSuggestion(suggestInstruction, range).then((code) => {
      if (code) setEditorNotice(suggestErrText(code));
      else setSuggestInstruction("");
    });
  };

  const acceptSuggestionIntoView = () => {
    setEditorNotice("");
    const code = editor.acceptSuggestion(
      editorHandleRef.current ? (edit) => editorHandleRef.current!.applyEdit(edit) : undefined,
    );
    if (code) setEditorNotice(suggestErrText(code));
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

  // After a mouse selection in the code/source view, surface a floating "送る" pill by
  // the selection. Scoped to CodeView because it queries that view's <code> element
  // (absent in md-preview / slides / image), so it stays inert elsewhere.
  const captureSelection = () => {
    // While the send modal is open, ignore mouseups — React portals bubble events through
    // the React tree, so a click inside the (body-portaled) modal reaches this handler and
    // would clear `sel` (the modal is gated on it), closing the modal on the first click.
    if (sendOpen) return;
    // A selection inside CodeMirror belongs to the editing surface, which reports
    // it from its own document (docs/44 §1.8). Walking the DOM for it would read
    // a virtualised, possibly truncated copy of the same selection.
    const editorEl = bodyRef.current?.querySelector(".file-editor-cm");
    const live = window.getSelection();
    if (editorEl && live?.anchorNode && editorEl.contains(live.anchorNode)) return;
    // Only one pill at a time: a selection outside the editor supersedes the
    // editor's, even when there is no code grid here to select in (a preview).
    const codeEl = bodyRef.current?.querySelector(".codeview .codegrid");
    const r = codeEl ? lineRangeOfSelection(codeEl) : null;
    if (!r) {
      setSel(null);
      return;
    }
    const rect = live!.getRangeAt(0).getBoundingClientRect();
    setSel({ ...r, x: Math.round(rect.left), y: Math.round(rect.top - 34), origin: "view" });
  };

  // The editing surface reports its own selection: line numbers and the quote
  // come from the CodeMirror document, and the pill is placed from coordsAtPos.
  const captureEditorSelection = (selection: EditorSelectionReport | null) => {
    if (sendOpen) return;
    setSel((prev) => editorPill(prev, selection, surfaces.editor));
  };

  // Leaving the editing surface drops its pill: the selection survives in the
  // editor state, but it is no longer on screen to send from.
  useEffect(() => {
    if (surfaces.editor) return;
    setSel((prev) => (prev?.origin === "editor" ? null : prev));
  }, [surfaces.editor]);

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
  // The probe's advisory (docs/44 §7.3): a polite status-line note, never an
  // alert, and never a phase change. The resolution panels already speak for
  // the alert phases, so the note yields to them.
  const externalObs = !editorAlert ? (editor.model?.externalObservation ?? null) : null;
  const externalNote = externalObs
    ? externalObs.kind === "changed"
      ? tr("editor.external.changed")
      : externalObs.kind === "same_as_buffer"
        ? tr("editor.external.same_as_buffer")
        : externalObs.kind === "missing"
          ? tr("editor.external.missing")
          : externalObs.kind === "uneditable"
            ? tr("editor.external.uneditable")
            : tr("editor.external.boundary")
    : "";

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
            {/* Non-Markdown keeps the view/edit tablist. Markdown drives the same
                pane layer from its own top-level button group below, so the two
                control kinds never appear together (docs/44 §1.1, §5). */}
            {fileMode.kind === "plain" && (
              <div
                className="ui-seg sm file-mode-tabs"
                role="tablist"
                aria-label={tr("editor.mode_group")}
                onKeyDown={onModeKeyDown}
              >
                <button
                  ref={viewTabRef}
                  type="button"
                  className={"seg-btn" + (paneMode === "view" ? " active" : "")}
                  role="tab"
                  aria-selected={paneMode === "view"}
                  tabIndex={paneMode === "view" ? 0 : -1}
                  onClick={() => changeMode("view")}
                >
                  {tr("editor.mode.view")}
                </button>
                <button
                  ref={editTabRef}
                  type="button"
                  className={"seg-btn" + (paneMode === "edit" ? " active" : "")}
                  role="tab"
                  aria-selected={paneMode === "edit"}
                  tabIndex={paneMode === "edit" ? 0 : -1}
                  onClick={() => changeMode("edit")}
                >
                  {tr("editor.mode.edit")}
                </button>
              </div>
            )}
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
              onClick={() => {
                setEditorNotice("");
                void editor.save();
              }}
              title={tr("editor.save_tip")}
            >
              <Icon name={phase === "saving" ? "loading" : "save"} spin={phase === "saving"} /> {tr("editor.save")}
            </button>
            <button
              type="button"
              className="file-save-btn file-suggest-btn"
              disabled={editorAlert || phase === "saving" || editor.suggesting}
              onClick={() => setSuggestOpen(true)}
              title={tr("editor.suggestion.button_tip")}
            >
              <Icon name={editor.suggesting ? "loading" : "sparkle"} spin={editor.suggesting} />{" "}
              {tr("editor.suggestion.button")}
            </button>
          </>
        )}
        {fileMode.kind === "diagram" && (
          <>
            <span
              ref={modeGroupRef}
              className="ui-seg sm md-toggle"
              role="group"
              aria-label={tr("view.diagram_display_mode")}
            >
              {diagramModeControls(fileMode, caps).map((control) => (
                <button
                  key={control.mode}
                  type="button"
                  aria-pressed={control.pressed}
                  className={"seg-btn" + (control.pressed ? " active" : "")}
                  onClick={() => changeDiagramMode(control.mode)}
                >
                  {control.mode === "figure"
                    ? tr("view.diagram")
                    : // 編集面が無いときは読み取り専用の「ソース」と名乗る
                      control.readOnlySource
                      ? tr("view.source")
                      : tr("editor.mode.edit")}
                </button>
              ))}
            </span>
            {surfaces.diagram && diagramState && (
              <span className="fi-meta muted">
                {diagramState.pages > 1
                  ? tr("view.diagram_page", { page: diagramState.page, pages: diagramState.pages })
                  : ""}
                {diagramState.scale !== 1 ? ` ${Math.round(diagramState.scale * 100)}%` : ""}
              </span>
            )}
          </>
        )}
        {fileMode.kind === "markdown" && (
          <>
            <span
              ref={modeGroupRef}
              className="ui-seg sm md-toggle"
              role="group"
              aria-label={tr("view.markdown_display_mode")}
            >
              {markdownModeControls(fileMode, caps).map((control) => (
                <button
                  key={control.mode}
                  type="button"
                  aria-pressed={control.pressed}
                  className={"seg-btn" + (control.pressed ? " active" : "")}
                  onClick={() => changeMarkdownMode(control.mode)}
                >
                  {control.mode === "split"
                    ? tr("editor.mode.split")
                    : control.mode === "preview"
                      ? tr("view.preview")
                      : // Without an edit surface this is the read-only source
                        // view the pane has always offered — say so.
                        control.readOnlySource
                        ? tr("view.source")
                        : tr("editor.mode.edit")}
                </button>
              ))}
            </span>
            {renderers.length > 0 && (
              <span className="ui-seg sm md-toggle" role="group" aria-label={tr("editor.renderer_group")}>
                {renderers.map((control) => (
                  <button
                    key={control.renderer}
                    type="button"
                    aria-pressed={control.pressed}
                    className={"seg-btn" + (control.pressed ? " active" : "")}
                    onClick={() => changeRenderer(control.renderer)}
                  >
                    {control.renderer === "slides" ? tr("view.slides") : tr("editor.renderer.doc")}
                  </button>
                ))}
              </span>
            )}
          </>
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
        {/* 図には朗読を出さない。読み上げる本文が無く（あるのは mxfile の XML）、
            押しても意味のある結果にならない —— 能力が無いなら操作要素を出さない。 */}
        {isText && !huge && !isDiagram && (
          <span className="ui-seg sm md-toggle">
            <button type="button" className="seg-btn" onClick={openReader} title={tr("view.open_reader_tip")}>
              <Icon name="book" /> {tr("view.read_aloud")}
            </button>
          </span>
        )}
        {/* One flex child so path + download wrap (fileinfo flex-wrap) together —
            never the lone download icon on its own line. */}
        <span className="fi-end">
          <span className="fi-path muted" title={filePath}>
            {filePath}
          </span>
          <a className="fi-dl" href={downloadURL(filePath)} download={baseName(filePath)} title={tr("view.download")}>
            <Icon name="cloud-download" />
          </a>
        </span>
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

        <div className="file-viewer-shell" hidden={!surfaces.source && !surfaces.preview}>
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
          ) : surfaces.preview === "slides" ? (
            <MarpView source={previewSource} />
          ) : surfaces.preview === "normal" ? (
            <div className="md-scroll">
              <MarkdownView source={previewSource} basePath={filePath} onOpenFile={showFile} onOpenDir={(path) => revealInFiles(path, { focus: true })} />
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

      {/* AI 提案（docs/44 Phase 4）。競合等のアラートパネルが出ているときは譲る
          （プローブ advisory と同じ優先順位）。レビュー段階は model.suggestion が持ち、
          staleness は revision から導出する。 */}
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

interface EditorSuggestPanelProps {
  model: FileEditorModel;
  suggestion: EditSuggestionEnvelope | null;
  suggesting: boolean;
  instruction: string;
  onInstructionChange(value: string): void;
  onSubmit(): void;
  onAccept(): void;
  onReject(): void;
  onClose(): void;
}

// AI 提案パネル（docs/44 Phase 4）。compose（指示文入力）→ 生成中 → レビュー
// （summary＋選択範囲→置換文の diff＋適用/却下）の3段を1つのオーバーレイで持つ。
// 競合パネルと違いエラーではないので role="alert" にはしない。適用可否は
// baseRevision と現在 bufferRevision の一致から導出し、stale なら適用を無効化する。
function EditorSuggestPanel(props: EditorSuggestPanelProps) {
  const tr = useT();
  const { model, suggestion } = props;
  if (suggestion) {
    const { range, replacement, summary } = suggestion.suggestion;
    const stale = suggestion.suggestion.baseRevision !== model.bufferRevision;
    const rows = stale ? [] : lineDiff(model.content.slice(range.from, range.to), replacement);
    return (
      <section className="file-editor-resolution file-editor-suggest" aria-label={tr("editor.suggestion.title")}>
        <h4>
          <Icon name="sparkle" /> {tr("editor.suggestion.title")}
        </h4>
        <p>{summary}</p>
        {stale ? (
          <p className="muted">{tr("editor.suggestion.stale")}</p>
        ) : (
          <div className="file-suggest-diff" aria-label={tr("editor.diff_aria")}>
            <pre>
              {rows.map((row, index) => (
                <span
                  key={index}
                  className={row.t === "del" ? "diff-mine" : row.t === "add" ? "diff-remote" : "diff-same"}
                >
                  {row.t === "del" ? "− " : row.t === "add" ? "+ " : "  "}
                  {row.text}
                  {"\n"}
                </span>
              ))}
            </pre>
          </div>
        )}
        <div className="file-editor-actions">
          <button type="button" autoFocus disabled={stale} onClick={props.onAccept}>
            {tr("editor.suggestion.apply")}
          </button>
          <button type="button" onClick={props.onReject}>
            {tr("editor.suggestion.reject")}
          </button>
          <button type="button" onClick={props.onClose}>
            {tr("editor.cancel")}
          </button>
        </div>
      </section>
    );
  }
  return (
    <section className="file-editor-resolution file-editor-suggest" aria-label={tr("editor.suggestion.title")}>
      <h4>
        <Icon name="sparkle" /> {tr("editor.suggestion.title")}
      </h4>
      <p className="muted">{tr("editor.suggestion.compose_hint")}</p>
      <textarea
        value={props.instruction}
        placeholder={tr("editor.suggestion.placeholder")}
        rows={3}
        autoFocus
        disabled={props.suggesting}
        onChange={(event) => props.onInstructionChange(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !props.suggesting) {
            event.preventDefault();
            props.onSubmit();
          }
        }}
      />
      <div className="file-editor-actions">
        <button
          type="button"
          disabled={props.suggesting || !props.instruction.trim()}
          onClick={props.onSubmit}
        >
          {props.suggesting ? (
            <>
              <Icon name="loading" spin /> {tr("editor.suggestion.generating")}
            </>
          ) : (
            tr("editor.suggestion.generate")
          )}
        </button>
        <button type="button" onClick={props.onClose}>
          {tr("editor.cancel")}
        </button>
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
      {/* `side` is the CSS identifier (diff-mine / diff-remote); the heading is a tr() label. */}
      <strong>{tr(side === "mine" ? "editor.diff.mine" : "editor.diff.remote")}</strong>
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
