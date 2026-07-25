import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { Compartment, EditorState, type Extension, type Transaction } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap, historyKeymap, indentWithTab } from "@codemirror/commands";
import { searchKeymap } from "@codemirror/search";
import { loadLanguageExtension } from "./languages.ts";
import { validateEditorBuffer, type BufferValidationError } from "./buffer.ts";
import { t } from "../../lib/i18n/index.ts";

interface CodeEditorProps {
  path: string;
  content: string;
  wrap: boolean;
  onChange(content: string): void;
  onSave(): void;
  onValidationError(error: BufferValidationError): void;
  onReady?(focus: () => void): void;
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

export function validateEditorInsertion(
  state: EditorState,
  from: number,
  to: number,
  text: string,
): BufferValidationError | null {
  return validateEditorBuffer(
    state.doc.sliceString(0, from) + text + state.doc.sliceString(to),
  );
}

export function CodeEditor({
  path,
  content,
  wrap,
  onChange,
  onSave,
  onValidationError,
  onReady,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const wrappingRef = useRef<Compartment | null>(null);
  if (!wrappingRef.current) wrappingRef.current = new Compartment();
  const callbacks = useRef({ onChange, onSave, onValidationError, onReady });
  callbacks.current = { onChange, onSave, onValidationError, onReady };

  useEffect(() => {
    if (!hostRef.current) return;
    const language = new Compartment();
    const wrapping = wrappingRef.current!;
    let validationQueued = false;
    const extensions: Extension[] = [
      basicSetup,
      language.of([]),
      wrapping.of(wrap ? EditorView.lineWrapping : []),
      EditorView.contentAttributes.of({
        "aria-label": t("editor.aria_label", { path }),
        "aria-multiline": "true",
        spellcheck: "false",
      }),
      // CodeMirror normalizes CR/CRLF to its internal LF line separator before a
      // Transaction exists. Inspect the raw DOM/IME/paste insertion here so CR is
      // rejected instead of silently normalized.
      EditorView.inputHandler.of((view, from, to, text) => {
        const error = validateEditorInsertion(view.state, from, to, text);
        if (!error) return false;
        if (!validationQueued) {
          validationQueued = true;
          queueMicrotask(() => {
            validationQueued = false;
            callbacks.current.onValidationError(error);
          });
        }
        return true;
      }),
      EditorState.transactionFilter.of((transaction) => {
        return filterBufferTransaction(transaction, (error) => {
          if (!validationQueued) {
            validationQueued = true;
            queueMicrotask(() => {
              validationQueued = false;
              callbacks.current.onValidationError(error);
            });
          }
        });
      }),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) callbacks.current.onChange(update.state.doc.toString());
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
        "&": { height: "100%", backgroundColor: "var(--surface, #111)" },
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

  return <div className="file-editor-cm" ref={hostRef} />;
}
