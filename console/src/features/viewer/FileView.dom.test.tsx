// Render tests for the File pane's Phase 3 modes (docs/log/44 §1.1, §1.8).
//
// These cover the wiring between "what the file is" and "what the pane shows" —
// the layer that the mode/pill unit tests cannot reach because it lives in
// FileView's render cycle. Every case here is a regression that shipped and was
// caught in review.
//
// What this file cannot check: jsdom has no layout, so the pill's position, the
// scroll landing of a line jump and anything else geometric still need a real
// browser (see src/test/domSetup.ts).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EditorView } from "@codemirror/view";
import { revisionOf } from "../editor/buffer.ts";
import { clearDirtyRegistryForTests, hasDirtyEditors } from "../editor/dirtyRegistry.ts";
import { clearEditorSlotsForTests } from "../editor/useFileEditor.ts";
import { clearFileModes } from "./fileModeMemory.ts";
import { clearScrollPos } from "./scrollMemory.ts";
import type { attachEditorScrollMemory as realAttach } from "./parts/editorScrollMemory.ts";

const MARP = "---\nmarp: true\n---\n\n# Deck\n\nbody\n";
const PLAIN_MD = "# Title\n\nalpha\nbeta\ngamma\n";

let served: { content: string; editable: boolean; nestedRepo?: boolean } = { content: PLAIN_MD, editable: true };

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const { content, editable, nestedRepo } = served;
    return {
      path: "repos/x/doc.md",
      size: content.length,
      binary: false,
      truncated: false,
      editable,
      editabilityReason: editable ? null : "read_only_root",
      content,
      ...(nestedRepo ? { nestedRepo } : {}),
      ...(editable ? { revision: revisionOf(content) } : {}),
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));
// The renderers themselves are covered by their own suites; here they only need
// to say which one the pane picked.
const previewProps: Record<string, unknown>[] = [];
vi.mock("./MarpView.tsx", () => ({
  MarpView: (props: Record<string, unknown>) => {
    previewProps.push({ ...props, renderer: "slides" });
    return <div data-surface="slides" />;
  },
}));
vi.mock("./MarkdownView.tsx", () => ({
  MarkdownView: (props: Record<string, unknown>) => {
    previewProps.push({ ...props, renderer: "preview" });
    return <div data-surface="preview" />;
  },
}));

// The editor's scroll memory needs layout jsdom does not have (parts/editorScrollMemory's own
// suite covers it); here only the restore decision FileView hands it is observed.
const editorMemory: { key: string; restore: boolean; inputRoot?: string }[] = [];
vi.mock("./parts/editorScrollMemory.ts", () => {
  // Typed against the real function, so a changed signature fails the typecheck here.
  const attachEditorScrollMemory: typeof realAttach = (_editor, key, restore, inputRoot) => {
    editorMemory.push({ key, restore, inputRoot: inputRoot?.className });
    return () => {};
  };
  return { attachEditorScrollMemory };
});

const { FileView } = await import("./FileView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(props: { targetLine?: number; openMode?: "view" | "edit" }): Promise<void> {
  await act(async () => {
    root!.render(<FileView filePath="repos/x/doc.md" paneId="pane-1" {...props} />);
  });
  // Let the file GET resolve and its state land.
  await act(async () => {
    await Promise.resolve();
  });
}

/** Labels of the pressed buttons, in DOM order — the pane's visible state. */
const pressed = () =>
  [...host.querySelectorAll('button[aria-pressed="true"]')].map((b) => b.textContent);

const editorVisible = () => {
  const shell = host.querySelector(".file-editor-shell");
  return !!shell && !shell.hasAttribute("hidden");
};

const surface = () => host.querySelector("[data-surface]")?.getAttribute("data-surface") ?? null;

// `.md-toggle` is shared with the read-aloud control, so address the groups by
// the accessible name each one carries.
const groupLabels = (label: string) => {
  const group = host.querySelector(`[aria-label="${label}"]`);
  return group ? [...group.querySelectorAll("button")].map((b) => b.textContent) : null;
};
const modeLabels = () => groupLabels("Markdown display mode");
const rendererLabels = () => groupLabels("Preview renderer");
const modeButton = (label: string) =>
  [...host.querySelectorAll('[aria-label="Markdown display mode"] button')].find(
    (b) => b.textContent === label,
  ) as HTMLButtonElement;

/** The live CodeMirror instance behind the editing surface. */
const editorView = () => EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;

const lastPreview = () => previewProps.at(-1);

beforeEach(() => {
  clearDirtyRegistryForTests();
  previewProps.length = 0;
  editorMemory.length = 0;
  served = { content: PLAIN_MD, editable: true };
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  clearDirtyRegistryForTests();
  // An unmount with unsaved edits keeps the buffer for the view to come back to.
  clearEditorSlotsForTests();
  // The mode and position are remembered per pane x file, and every case uses the same pair.
  clearFileModes();
  clearScrollPos();
});

describe("markdown mode controls", () => {
  it("shows the three-mode group instead of the view/edit tablist", async () => {
    await render({});
    expect(host.querySelector('[role="tablist"]')).toBeNull();
    expect(modeLabels()).toEqual(["Edit", "Preview", "Split"]);
  });

  it("falls back to source/preview when the file cannot be edited", async () => {
    served = { content: PLAIN_MD, editable: false };
    await render({});
    expect(modeLabels()).toEqual(["Source", "Preview"]);
    expect(host.querySelector(".file-save-btn")).toBeNull();
  });
});

describe("a citation that arrives after the file is already open", () => {
  it("switches to the source surface so the cited row exists", async () => {
    // Regression: the mode was keyed on the loaded file alone, so retargeting the
    // pane at a line of the file it already showed left it in the preview and the
    // cited row was never drawn (docs/log/44 §1.8).
    await render({});
    expect(pressed()).toEqual(["Preview"]);
    expect(editorVisible()).toBe(false);

    await render({ targetLine: 3 });
    expect(pressed()).toEqual(["Edit"]);
    expect(editorVisible()).toBe(true);
  });

  it("opens straight into the source surface when the citation comes first", async () => {
    await render({ targetLine: 3 });
    expect(pressed()).toEqual(["Edit"]);
  });
});

describe("an opener that names the mode (変更リストの「編集」/「表示」)", () => {
  it("opens the edit surface on request", async () => {
    await render({ openMode: "edit" });
    expect(pressed()).toEqual(["Edit"]);
    expect(editorVisible()).toBe(true);
  });

  it("switches a pane that already shows the file in preview", async () => {
    // Same shape as the citation case above: retargeting the pane leaves the
    // loaded file untouched, so only the request is new.
    await render({});
    expect(pressed()).toEqual(["Preview"]);
    await render({ openMode: "edit" });
    expect(pressed()).toEqual(["Edit"]);
  });

  it("keeps the preview when the file cannot be edited", async () => {
    served = { content: PLAIN_MD, editable: false };
    await render({ openMode: "edit" });
    expect(pressed()).toEqual(["Preview"]);
    expect(editorVisible()).toBe(false);
  });

  it("leaves the mode alone once the user picks another one", async () => {
    await render({ openMode: "edit" });
    await act(async () => {
      modeButton("Preview").click();
    });
    expect(pressed()).toEqual(["Preview"]);
  });
});

describe("a Marp deck", () => {
  it("opens as slides without waiting out the preview debounce", async () => {
    // Regression: the preview source was debounced on the file path, which does
    // not change when the GET lands, so the initial Marp check ran on an empty
    // document. The deck opened in the normal preview and reconcile kept it
    // there. Nothing here advances timers — the check must be right at once.
    served = { content: MARP, editable: true };
    await render({});
    expect(pressed()).toEqual(["Preview", "Slides"]);
    expect(surface()).toBe("slides");
  });

  it("offers the document renderer beside slides", async () => {
    served = { content: MARP, editable: true };
    await render({});
    expect(rendererLabels()).toEqual(["Document", "Slides"]);
  });

  it("keeps a plain document out of the renderer group", async () => {
    await render({});
    expect(rendererLabels()).toBeNull();
  });
});

describe("the editing surface", () => {
  it("stays mounted while the preview is showing, so undo history survives", async () => {
    await render({});
    expect(host.querySelector(".file-editor-cm .cm-editor")).not.toBeNull();
    expect(editorVisible()).toBe(false);
  });

  it("shares the pane with the preview in split", async () => {
    await render({});
    await act(async () => {
      modeButton("Split").click();
    });
    expect(editorVisible()).toBe(true);
    expect(surface()).toBe("preview");
    expect(host.querySelector(".file-surfaces")?.className).toContain("is-split");
  });

  it("takes focus when the mode is chosen, including split to edit", async () => {
    // Regression: focus was driven by the surface becoming visible, and split and
    // edit both show it — so choosing edit from split moved no focus at all.
    await render({});
    await act(async () => modeButton("Split").click());
    await act(async () => {
      await Promise.resolve();
    });
    const content = host.querySelector(".cm-content") as HTMLElement;
    expect(host.contains(document.activeElement)).toBe(true);

    // Move focus away, then ask for edit while the surface is already on screen.
    modeButton("Split").focus();
    expect(document.activeElement).toBe(modeButton("Split"));
    await act(async () => modeButton("Edit").click());
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.activeElement).toBe(content);
  });

  it("drops a focus request that a later selection cancelled", async () => {
    // Regression: the request was a latch, so asking for edit and immediately
    // leaving again left it set. Nothing consumed it until the surface next
    // appeared — and when that was a citation rather than a selection, the
    // stale request stole focus from wherever the user was.
    await render({});
    await act(async () => {
      modeButton("Edit").click();
      modeButton("Preview").click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(editorVisible()).toBe(false);

    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();
    await render({ targetLine: 3 });
    expect(editorVisible()).toBe(true);
    expect(document.activeElement).toBe(outside);
    outside.remove();
  });

  it("leaves focus on the renderer group when only the renderer changes", async () => {
    // Regression: renderer selection went through the same path as mode
    // selection, so picking Document/Slides while split was showing yanked focus
    // into CodeMirror — away from the button group the user was operating.
    served = { content: MARP, editable: true };
    await render({});
    await act(async () => modeButton("Split").click());
    await act(async () => {
      await Promise.resolve();
    });

    const document_ = [...host.querySelectorAll('[aria-label="Preview renderer"] button')].find(
      (b) => b.textContent === "Document",
    ) as HTMLButtonElement;
    document_.focus();
    await act(async () => document_.click());
    await act(async () => {
      await Promise.resolve();
    });
    expect(surface()).toBe("preview");
    expect(editorVisible()).toBe(true);
    expect(document.activeElement).toBe(document_);
  });

  it("does not take focus merely because a citation opened the file", async () => {
    // Opening a file must not pull focus out of whatever the user was typing in.
    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();
    await render({ targetLine: 3 });
    expect(editorVisible()).toBe(true);
    expect(document.activeElement).toBe(outside);
    outside.remove();
  });
});

// docs/log/44 §6 Phase 3: the acceptance items that are about not losing what the
// pane already did.
describe("reuse of the existing rendering assets", () => {
  it("keeps the normal preview reachable on a Marp deck", async () => {
    // The whole reason the renderer is a separate axis: a deck must still be
    // readable as a document, which the pre-Phase-3 pane offered as `preview`.
    served = { content: MARP, editable: true };
    await render({});
    expect(surface()).toBe("slides");

    const document_ = [...host.querySelectorAll('[aria-label="Preview renderer"] button')].find(
      (b) => b.textContent === "Document",
    ) as HTMLButtonElement;
    await act(async () => document_.click());
    expect(surface()).toBe("preview");
    expect(pressed()).toEqual(["Preview", "Document"]);
  });

  it("switches the renderer of the right-hand side while split is showing", async () => {
    served = { content: MARP, editable: true };
    await render({});
    await act(async () => modeButton("Split").click());
    expect(editorVisible()).toBe(true);
    expect(surface()).toBe("slides");

    const document_ = [...host.querySelectorAll('[aria-label="Preview renderer"] button')].find(
      (b) => b.textContent === "Document",
    ) as HTMLButtonElement;
    await act(async () => document_.click());
    expect(editorVisible()).toBe(true);
    expect(surface()).toBe("preview");
  });

  it("hands the preview the buffer and the same link wiring as before", async () => {
    await render({});
    expect(lastPreview()).toMatchObject({
      source: PLAIN_MD,
      basePath: "repos/x/doc.md",
      // `#N` is read against the working copy the file lives in (#1900).
      repo: "x",
    });
    // The link handlers are what make relative links and mermaid-bearing docs
    // behave the same as in the read-only pane.
    expect(typeof lastPreview()!.onOpenFile).toBe("function");
    expect(typeof lastPreview()!.onOpenDir).toBe("function");
  });

  it("turns ticket links on from the file's repository, and off for a nested Git root", async () => {
    const { useReposStore } = await import("../repos/store.ts");
    useReposStore.setState({ repos: [{ name: "x", provider: "github", remote: "github.com", remotePath: "octo/x" }] });
    await render({});
    expect(lastPreview()).toMatchObject({ repo: "x", workItemRefs: true });

    // The Agent saw a .git below repos/x on the way to the file: its origin is not x's.
    served = { content: PLAIN_MD, editable: true, nestedRepo: true };
    previewProps.length = 0;
    await act(async () => root?.unmount());
    root = createRoot(host);
    await render({});
    expect(lastPreview()).toMatchObject({ workItemRefs: false });
    useReposStore.setState({ repos: [] });
  });
});

describe("the buffer behind the preview", () => {
  it("renders unsaved edits after the debounce, not before", async () => {
    await render({});
    await act(async () => modeButton("Split").click());
    expect(lastPreview()!.source).toBe(PLAIN_MD);

    vi.useFakeTimers();
    try {
      await act(async () => {
        editorView().dispatch({ changes: { from: 0, insert: "# Edited\n" } });
      });
      // The keystroke is in the buffer immediately, but the preview waits.
      expect(editorView().state.doc.toString().startsWith("# Edited")).toBe(true);
      expect(lastPreview()!.source).toBe(PLAIN_MD);

      await act(async () => {
        vi.advanceTimersByTime(250);
      });
      expect(lastPreview()!.source).toBe("# Edited\n" + PLAIN_MD);
    } finally {
      vi.useRealTimers();
    }
  });

  it("registers a dirty buffer with the navigation guard", async () => {
    // Markdown panes join the same dirty registry as code panes (docs/log/44 §1.1),
    // so every navigation guard covers them without a second mechanism.
    await render({});
    expect(hasDirtyEditors()).toBe(false);
    await act(async () => {
      editorView().dispatch({ changes: { from: 0, insert: "x" } });
    });
    expect(hasDirtyEditors()).toBe(true);
  });

  it("keeps an unsaved edit when the view is unmounted and comes back (tab switch)", async () => {
    // A tabbed cell renders only its selected view, so switching away unmounts FileView.
    await render({});
    await act(async () => {
      editorView().dispatch({ changes: { from: 0, insert: "# Edited\n" } });
    });
    act(() => root!.unmount());
    expect(hasDirtyEditors()).toBe(true); // reload / close / logout still ask

    root = createRoot(host);
    await render({});
    expect(editorView().state.doc.toString()).toBe("# Edited\n" + PLAIN_MD);
    expect(hasDirtyEditors()).toBe(true);
  });

  it("comes back to the mode it was left in after a tab switch", async () => {
    for (const mode of ["Split", "Edit", "Preview"]) {
      await render({});
      await act(async () => modeButton(mode).click());
      act(() => root!.unmount());
      root = createRoot(host);
      await render({});
      expect(pressed()).toContain(mode);
    }
  });

  it("lets a different opener request override the remembered mode", async () => {
    await render({});
    await act(async () => modeButton("Split").click());
    act(() => root!.unmount());
    // A citation now names a line: it needs the source row, not the remembered split.
    root = createRoot(host);
    await render({ targetLine: 3 });
    expect(pressed()).toContain("Edit");
    expect(pressed()).not.toContain("Split");
  });

  it("keeps the mode remembered under a request when the same request comes back", async () => {
    // The request lives in the layout descriptor, so a remount sees it again; it is not new.
    await render({ openMode: "edit" });
    await act(async () => modeButton("Preview").click());
    act(() => root!.unmount());
    root = createRoot(host);
    await render({ openMode: "edit" });
    expect(pressed()).toContain("Preview");
  });

  it("clamps a remembered mode to what the file allows now", async () => {
    await render({});
    await act(async () => modeButton("Split").click());
    act(() => root!.unmount());
    served = { content: PLAIN_MD, editable: false }; // became read-only meanwhile
    root = createRoot(host);
    await render({});
    expect(pressed()).toContain("Preview");
    expect(modeLabels()).toEqual(["Source", "Preview"]);
  });

  it("restores the editor's position unless a synced split takes it from the preview", async () => {
    await render({});
    // Preview -> Split: the scroll sync positions the editor from the preview.
    await act(async () => modeButton("Split").click());
    expect(editorMemory.at(-1)).toMatchObject({ key: "pane-1\u0000repos/x/doc.md\u0000editor", restore: false });
    // Reader input anywhere in the pane ends a restore: the split's preview, the header, the
    // suggestion panel.
    expect(editorMemory.at(-1)?.inputRoot).toBe("fileview");
    // Edit, then Split from the editor: the editor's own place is the one to keep.
    await act(async () => modeButton("Edit").click());
    expect(editorMemory.at(-1)).toMatchObject({ restore: true, inputRoot: "fileview" });
    await act(async () => modeButton("Split").click());
    expect(editorMemory.at(-1)).toMatchObject({ restore: true, inputRoot: "fileview" });
    // Back from another tab straight into the split: the editor leads.
    act(() => root!.unmount());
    editorMemory.length = 0;
    root = createRoot(host);
    await render({});
    expect(editorMemory.at(-1)?.restore).toBe(true);
  });

  it("only records the editor's position when the open file is cited at a new line", async () => {
    await render({ openMode: "edit" });
    expect(editorMemory.at(-1)?.restore).toBe(true);
    editorMemory.length = 0;
    await render({ openMode: "edit", targetLine: 50 });
    expect(editorMemory.at(-1)?.restore).toBe(false); // the citation's line wins
    // A later mode change restores as usual: what it restores is where the citation left it.
    await act(async () => modeButton("Split").click());
    expect(editorMemory.at(-1)?.restore).toBe(true);
  });

  it("restores the editor in a slides split, which has no scroll sync", async () => {
    served = { content: MARP, editable: true };
    await render({});
    await act(async () => {
      await new Promise((r) => setTimeout(r, 400)); // the Marp detection is debounced
    });
    await act(async () => modeButton("Split").click());
    expect(lastPreview()?.renderer).toBe("slides");
    expect(editorMemory.at(-1)?.restore).toBe(true);
  });

  it("puts Markdown edits through the same buffer validator", async () => {
    // The validator itself is covered in buffer.test.ts; what matters here is
    // that a Markdown pane is wired into it. NUL is the probe rather than CR:
    // EditorState.toText normalises CRLF to LF before any transaction filter
    // sees it, so a programmatic dispatch cannot carry a CR (that is what the
    // separate clipboard filter is for).
    await render({});
    await act(async () => modeButton("Edit").click());
    await act(async () => {
      editorView().dispatch({ changes: { from: 0, insert: "a\u0000b" } });
    });
    expect(editorView().state.doc.toString()).toBe(PLAIN_MD);
    expect(hasDirtyEditors()).toBe(false);
    await act(async () => {
      await Promise.resolve();
    });
    expect(host.querySelector(".file-editor-status")?.textContent).toContain("NUL");
  });
});
