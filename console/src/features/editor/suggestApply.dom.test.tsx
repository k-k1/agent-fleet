// Phase 4 acceptance (docs/log/44 §4.2/§4.3): an accepted AI suggestion reaches the
// document as ONE ranged, undoable transaction through the CodeEditorHandle, and
// a validator-violating edit is dropped by the shared transaction filter.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EditorView } from "@codemirror/view";
import { undo, undoDepth } from "@codemirror/commands";

vi.mock("../../core/api/client.ts", () => ({
  rel: (p: string) => p,
}));

const { CodeEditor } = await import("./CodeEditor.tsx");
type Handle = import("./CodeEditor.tsx").CodeEditorHandle;

afterEach(() => {
  document.body.innerHTML = "";
});

describe("CodeEditorHandle.applyEdit", () => {
  let root: Root | null = null;

  const mount = async (content: string, onChange: (c: string) => void = () => {}) => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    let handle: Handle | null = null;
    await act(async () => {
      root!.render(
        <CodeEditor
          path="repos/a.txt"
          content={content}
          wrap={false}
          onChange={onChange}
          onSave={() => {}}
          onValidationError={() => {}}
          onHandle={(h) => {
            if (h) handle = h;
          }}
        />,
      );
    });
    const view = EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
    return { handle: handle!, view };
  };

  const unmount = async () => {
    await act(async () => root!.unmount());
    delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  };

  it("applies a ranged replacement as one undoable step and reports it through onChange", async () => {
    const changes: string[] = [];
    const { handle, view } = await mount("# title\nbody\n", (c) => changes.push(c));
    try {
      let ok = false;
      await act(async () => {
        ok = handle.applyEdit({ from: 0, to: 7, insert: "# concrete title" });
      });
      expect(ok).toBe(true);
      expect(view.state.doc.toString()).toBe("# concrete title\nbody\n");
      expect(changes.at(-1)).toBe("# concrete title\nbody\n");
      // カーソルは置換末尾へ。
      expect(view.state.selection.main.head).toBe("# concrete title".length);
      // Ctrl+Z 1回で受諾前へ戻る（外部追従と違い、提案の適用は通常の編集）。
      expect(undoDepth(view.state)).toBe(1);
      await act(async () => {
        undo(view);
      });
      expect(view.state.doc.toString()).toBe("# title\nbody\n");
    } finally {
      await unmount();
    }
  });

  // CR/CRLF は CodeMirror が dispatch 時に LF へ正規化してしまいフィルタに届かない。
  // 改行契約は適用境界の checkSuggestion が dispatch 前に弾く（suggest.test.ts）。
  // ここではフィルタが実際に観測する違反（NUL）で第二防衛線を固定する。
  it("drops a validator-violating edit via the shared transaction filter", async () => {
    const { handle, view } = await mount("base\n");
    try {
      let ok = true;
      await act(async () => {
        ok = handle.applyEdit({ from: 0, to: 4, insert: "bad\u0000" });
      });
      expect(ok).toBe(false);
      expect(view.state.doc.toString()).toBe("base\n");
    } finally {
      await unmount();
    }
  });

  it("reports the current selection in UTF-16 offsets", async () => {
    const { handle, view } = await mount("hello\n");
    try {
      await act(async () => {
        view.dispatch({ selection: { anchor: 1, head: 4 } });
      });
      expect(handle.selection()).toEqual({ from: 1, to: 4 });
    } finally {
      await unmount();
    }
  });
});
