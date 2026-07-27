import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import {
  Compartment,
  EditorState,
  Prec,
  type Extension,
  type Transaction,
} from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap, historyKeymap, indentWithTab } from "@codemirror/commands";
import { searchKeymap } from "@codemirror/search";
import { loadLanguageExtension } from "./languages.ts";
import { validateEditorBuffer, type BufferValidationError } from "./buffer.ts";
import { lineStartOf, selectionRangeOf, type EditorSelectionRange } from "./selection.ts";
import { t } from "../../lib/i18n/index.ts";

/** A selection, plus where to put UI beside it. `coords` is null when the
 *  selection start is scrolled out of the rendered range. */
export interface EditorSelectionReport extends EditorSelectionRange {
  coords: { left: number; top: number } | null;
}

interface CodeEditorProps {
  path: string;
  content: string;
  wrap: boolean;
  /** 1-based line to reveal — a citation that opened this file (docs/44 §1.8). */
  targetLine?: number;
  onChange(content: string): void;
  onSave(): void;
  onValidationError(error: BufferValidationError): void;
  onReady?(focus: () => void): void;
  /** Fires on every selection change with the quotable selection, or null. */
  onSelectionChange?(selection: EditorSelectionReport | null): void;
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
  onSelectionChange,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const wrappingRef = useRef<Compartment | null>(null);
  if (!wrappingRef.current) wrappingRef.current = new Compartment();
  const callbacks = useRef({ onChange, onSave, onValidationError, onReady, onSelectionChange });
  callbacks.current = { onChange, onSave, onValidationError, onReady, onSelectionChange };

  useEffect(() => {
    if (!hostRef.current) return;
    const language = new Compartment();
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
        if (!update.docChanged && !update.selectionSet && !update.geometryChanged) return;
        const report = callbacks.current.onSelectionChange;
        if (!report) return;
        const range = selectionRangeOf(update.state);
        if (!range) return report(null);
        const coords = update.view.coordsAtPos(range.from);
        report({ ...range, coords: coords ? { left: coords.left, top: coords.top } : null });
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
    const view = new EditorView({
      state: EditorState.create({ doc: content, extensions }),
      parent: hostRef.current,
    });
    viewRef.current = view;
    callbacks.current.onReady?.(() => {
      view.requestMeasure();
      view.focus();
    });
    let alive = true;
    void loadLanguageExtension(path).then((extension) => {
      if (alive) view.dispatch({ effects: language.reconfigure(extension) });
    });
    return () => {
      alive = false;
      viewRef.current = null;
      view.destroy();
    };
    // A file identity owns one CodeMirror instance. Callback/content changes are
    // synchronized below without discarding undo history.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

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
