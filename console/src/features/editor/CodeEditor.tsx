import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import {
  Compartment,
  EditorState,
  Prec,
  type Extension,
  type StateEffect,
  type Transaction,
} from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap, historyKeymap, indentWithTab } from "@codemirror/commands";
import { searchKeymap } from "@codemirror/search";
import { loadLanguageExtension } from "./languages.ts";
import { validateEditorBuffer, type BufferValidationError } from "./buffer.ts";
import { lineStartOf, selectionRangeOf, type EditorSelectionReport } from "./selection.ts";
import { t } from "../../lib/i18n/index.ts";

/** Imperative surface for the AI suggestion flow (docs/log/44 §4): read the current
 *  selection when a suggestion is requested, and apply an accepted replacement
 *  as ONE ranged transaction — undoable like a user edit, and filtered by the
 *  shared buffer validator like every other transaction. */
export interface CodeEditorHandle {
  /** Current main selection as UTF-16 code-unit offsets into the document. */
  selection(): { from: number; to: number };
  /** Apply a ranged edit; returns false when the transaction was rejected. */
  applyEdit(edit: { from: number; to: number; insert: string }): boolean;
}

interface CodeEditorProps {
  path: string;
  content: string;
  wrap: boolean;
  /** 1-based line to reveal — a citation that opened this file (docs/log/44 §1.8). */
  targetLine?: number;
  onChange(content: string): void;
  onSave(): void;
  onValidationError(error: BufferValidationError): void;
  onReady?(focus: () => void): void;
  /** Receives the imperative handle on mount and null on unmount. */
  onHandle?(handle: CodeEditorHandle | null): void;
  /** Fires on every selection change with the quotable selection, or null. */
  onSelectionChange?(selection: EditorSelectionReport | null): void;
  /** Bumped when a clean buffer auto-followed an external change (docs/log/44
   *  §7.4). The document is swapped by rebuilding the editor state — not by an
   *  edit transaction — so undo cannot roll the external content back, and the
   *  cursor and scroll positions are restored by line number. */
  externalEpoch?: number;
}

export function filterBufferTransaction(
  transaction: Transaction,
  reject: (error: BufferValidationError) => void,
): Transaction | readonly Transaction[] {
  if (!transaction.docChanged) return transaction;
  const error = validateEditorBuffer(transaction.newDoc.toString());
  if (!error) return transaction;
  reject(error);
  return [];
}

const REJECTED_CLIPBOARD_TEXT = "\u0000";

export function bufferValidationExtensions(
  reject: (error: BufferValidationError) => void,
): Extension {
  let validationQueued = false;
  const report = (error: BufferValidationError) => {
    if (validationQueued) return;
    validationQueued = true;
    queueMicrotask(() => {
      validationQueued = false;
      reject(error);
    });
  };
  return [
    // CodeMirror's paste implementation calls clipboardInputFilter with the raw
    // clipboard string before EditorState.toText normalizes CR/CRLF to LF. Replace
    // rejected input with a sentinel that the transaction filter below refuses, so
    // invalid clipboard data is reported and never reaches the document.
    Prec.highest(
      EditorView.clipboardInputFilter.of((text) => {
        const error = validateEditorBuffer(text);
        if (!error) return text;
        report(error);
        return REJECTED_CLIPBOARD_TEXT;
      }),
    ),
    EditorState.transactionFilter.of((transaction) => {
      return filterBufferTransaction(transaction, report);
    }),
  ];
}

/** Replace the document without leaving the old content in the undo history
 *  (docs/log/44 §7.4): a change transaction would let Ctrl+Z resurrect — and then
 *  save — the pre-follow text, so the whole editor state is rebuilt instead.
 *  Cursor and scroll are restored by line number, clamped to the new document.
 *  `reconfigure` re-applies compartment values (language, wrapping) that the
 *  fresh state resets to their initial configuration. */
export function followDocument(
  view: EditorView,
  content: string,
  extensions: Extension,
  reconfigure: readonly StateEffect<unknown>[] = [],
): void {
  const oldState = view.state;
  const cursorLine = oldState.doc.lineAt(oldState.selection.main.head).number;
  let topLine = cursorLine;
  try {
    topLine = oldState.doc.lineAt(view.lineBlockAtHeight(view.scrollDOM.scrollTop).from).number;
  } catch {
    // No layout (headless tests) — fall back to keeping the cursor line visible.
  }
  view.setState(EditorState.create({ doc: content, extensions }));
  const doc = view.state.doc;
  const cursor = doc.line(Math.max(1, Math.min(cursorLine, doc.lines))).from;
  const top = doc.line(Math.max(1, Math.min(topLine, doc.lines))).from;
  view.dispatch({
    selection: { anchor: cursor },
    effects: [...reconfigure, EditorView.scrollIntoView(top, { y: "start" })],
  });
}

/** Scroll a 1-based line into view and put the cursor on it. The cursor is what
 *  marks the line: `basicSetup`'s active-line highlight follows it, which is the
 *  editing surface's equivalent of CodeView's target-line row. */
export function revealLine(view: EditorView, line: number): void {
  const position = lineStartOf(view.state, line);
  view.dispatch({
    selection: { anchor: position },
    effects: EditorView.scrollIntoView(position, { y: "center" }),
  });
}

export function CodeEditor({
  path,
  content,
  wrap,
  targetLine,
  onChange,
  onSave,
  onValidationError,
  onReady,
  onHandle,
  onSelectionChange,
  externalEpoch,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const wrappingRef = useRef<Compartment | null>(null);
  if (!wrappingRef.current) wrappingRef.current = new Compartment();
  const languageRef = useRef<Compartment | null>(null);
  if (!languageRef.current) languageRef.current = new Compartment();
  const languageExtRef = useRef<Extension>([]);
  const extensionsRef = useRef<Extension | null>(null);
  const appliedEpochRef = useRef(externalEpoch ?? 0);
  const callbacks = useRef({ onChange, onSave, onValidationError, onReady, onHandle, onSelectionChange, externalEpoch });
  callbacks.current = { onChange, onSave, onValidationError, onReady, onHandle, onSelectionChange, externalEpoch };

  useEffect(() => {
    if (!hostRef.current) return;
    const language = languageRef.current!;
    const wrapping = wrappingRef.current!;
    const extensions: Extension[] = [
      basicSetup,
      language.of([]),
      wrapping.of(wrap ? EditorView.lineWrapping : []),
      EditorView.contentAttributes.of({
        "aria-label": t("editor.aria_label", { path }),
        "aria-multiline": "true",
        spellcheck: "false",
      }),
      bufferValidationExtensions((error) => callbacks.current.onValidationError(error)),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) callbacks.current.onChange(update.state.doc.toString());
        // Geometry matters too: scrolling moves the selection's screen position
        // (and can push it out of the rendered range), so any UI anchored to it
        // has to be told. The quote itself always comes from the document.
        const moved = update.docChanged || update.selectionSet;
        if (!moved && !update.geometryChanged) return;
        const report = callbacks.current.onSelectionChange;
        if (!report) return;
        const reason = moved ? "selection" : "geometry";
        const range = selectionRangeOf(update.state);
        if (!range) return report(null);
        const coords = update.view.coordsAtPos(range.from);
        report({ ...range, coords: coords ? { left: coords.left, top: coords.top } : null, reason });
      }),
      keymap.of([
        {
          key: "Mod-s",
          preventDefault: true,
          run: () => {
            callbacks.current.onSave();
            return true;
          },
        },
        indentWithTab,
        ...defaultKeymap,
        ...historyKeymap,
        ...searchKeymap,
      ]),
      EditorView.theme({
        // Keep the editor on the same configurable surface as FileView rather
        // than falling back to CodeMirror's dark default in a light viewer.
        "&": { height: "100%", backgroundColor: "var(--viewer-bg, var(--bg))" },
        ".cm-scroller": { fontFamily: "var(--viewer-font)", fontSize: "var(--viewer-size)" },
        ".cm-content": { minHeight: "100%" },
      }),
    ];
    extensionsRef.current = extensions;
    // A fresh mount starts a fresh model, so its follow epoch is the baseline —
    // without this, a pane whose previous file had auto-followed would treat the
    // new file's epoch 0 as a change and pointlessly rebuild the state.
    appliedEpochRef.current = callbacks.current.externalEpoch ?? 0;
    languageExtRef.current = [];
    const view = new EditorView({
      state: EditorState.create({ doc: content, extensions }),
      parent: hostRef.current,
    });
    viewRef.current = view;
    callbacks.current.onReady?.(() => {
      view.requestMeasure();
      view.focus();
    });
    callbacks.current.onHandle?.({
      selection: () => {
        const range = view.state.selection.main;
        return { from: range.from, to: range.to };
      },
      applyEdit: ({ from, to, insert }) => {
        const before = view.state.doc;
        view.dispatch({
          changes: { from, to, insert },
          selection: { anchor: from + insert.length },
          scrollIntoView: true,
          userEvent: "input.suggest",
        });
        return view.state.doc !== before;
      },
    });
    let alive = true;
    void loadLanguageExtension(path).then((extension) => {
      if (!alive) return;
      languageExtRef.current = extension;
      view.dispatch({ effects: language.reconfigure(extension) });
    });
    return () => {
      alive = false;
      callbacks.current.onHandle?.(null);
      viewRef.current = null;
      view.destroy();
    };
    // A file identity owns one CodeMirror instance. Callback/content changes are
    // synchronized below without discarding undo history.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  // Declared before the content-sync effect below so an auto-follow captures
  // the cursor/scroll lines from the OLD document; the sync effect then finds
  // the document already replaced and does nothing.
  useEffect(() => {
    const epoch = externalEpoch ?? 0;
    if (appliedEpochRef.current === epoch) return;
    appliedEpochRef.current = epoch;
    const view = viewRef.current;
    if (!view || !extensionsRef.current) return;
    followDocument(view, content, extensionsRef.current, [
      languageRef.current!.reconfigure(languageExtRef.current),
      wrappingRef.current!.reconfigure(wrap ? EditorView.lineWrapping : []),
    ]);
    // The follow consumes the epoch bump alone; content/wrap are read from the
    // same commit and have their own sync effects.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [externalEpoch]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view || view.state.doc.toString() === content) return;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: content } });
  }, [content]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch({
      effects: wrappingRef.current!.reconfigure(wrap ? EditorView.lineWrapping : []),
    });
  }, [wrap]);

  // Reveal the cited line. `path` is a dependency so a citation that opens a
  // different file still jumps: the view above is rebuilt for the new path and
  // this effect, declared after it, runs against the fresh instance.
  useEffect(() => {
    const view = viewRef.current;
    if (!view || !targetLine) return;
    revealLine(view, targetLine);
  }, [path, targetLine]);

  return <div className="file-editor-cm" ref={hostRef} />;
}
