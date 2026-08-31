// Phase 3.5 acceptance (docs/log/44 §7.4): a clean auto-follow must not leave the
// replaced content in the undo history — Ctrl+Z after a follow may not
// resurrect (and then let the user save) the pre-follow text — and cursor and
// scroll are restored by line number, clamped to the new document.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { history, undo, undoDepth } from "@codemirror/commands";

vi.mock("../../core/api/client.ts", () => ({
  rel: (p: string) => p,
}));

const { CodeEditor, followDocument } = await import("./CodeEditor.tsx");

afterEach(() => {
  document.body.innerHTML = "";
});

describe("followDocument", () => {
  const mount = (doc: string) => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const extensions = [history()];
    const view = new EditorView({ state: EditorState.create({ doc, extensions }), parent });
    return { view, extensions };
  };

  it("leaves no undo path back to the replaced content", () => {
    const { view, extensions } = mount("one\ntwo\n");
    view.dispatch({ changes: { from: 0, insert: "typed " } });
    expect(undoDepth(view.state)).toBeGreaterThan(0);

    followDocument(view, "external\ncontent\n", extensions);

    expect(view.state.doc.toString()).toBe("external\ncontent\n");
    expect(undoDepth(view.state)).toBe(0);
    undo(view);
    expect(view.state.doc.toString()).toBe("external\ncontent\n");
    view.destroy();
  });

  it("restores the cursor by line number, clamped to the new document", () => {
    const { view, extensions } = mount("a\nb\nc\nd\n");
    view.dispatch({ selection: { anchor: view.state.doc.line(3).from } });

    followDocument(view, "x\ny\nz\nw\n", extensions);
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(3);

    followDocument(view, "only\n", extensions);
    // "only\n" has 2 document lines (the trailing one empty); line 3 clamps.
    expect(view.state.doc.lineAt(view.state.selection.main.head).number).toBe(2);
    view.destroy();
  });
});

describe("CodeEditor externalEpoch", () => {
  let root: Root | null = null;
  let host: HTMLElement;

  const render = async (props: { content: string; externalEpoch: number }) => {
    await act(async () => {
      root!.render(
        <CodeEditor
          path="repos/a.txt"
          content={props.content}
          wrap={false}
          externalEpoch={props.externalEpoch}
          onChange={() => {}}
          onSave={() => {}}
          onValidationError={() => {}}
        />,
      );
    });
  };

  const view = () => EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;

  it("an epoch bump swaps the document without an undoable step; a plain content change stays undoable", async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    try {
      await render({ content: "base\n", externalEpoch: 0 });
      await act(async () => {
        view().dispatch({ changes: { from: 0, insert: "typed " } });
      });
      expect(undoDepth(view().state)).toBeGreaterThan(0);

      // Auto-follow: model rebuilt, epoch bumped, new content arrives together.
      await render({ content: "external\n", externalEpoch: 1 });
      expect(view().state.doc.toString()).toBe("external\n");
      expect(undoDepth(view().state)).toBe(0);
      await act(async () => {
        undo(view());
      });
      expect(view().state.doc.toString()).toBe("external\n");

      // An ordinary content prop change (remote adoption, discard) keeps the
      // Phase 2 behaviour: it goes through a transaction and stays undoable.
      await render({ content: "adopted\n", externalEpoch: 1 });
      expect(view().state.doc.toString()).toBe("adopted\n");
      expect(undoDepth(view().state)).toBeGreaterThan(0);
    } finally {
      await act(async () => root!.unmount());
      delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
    }
  });
});
