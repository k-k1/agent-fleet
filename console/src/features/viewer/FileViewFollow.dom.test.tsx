// FileView wiring for the external-change probe (docs/log/44 §7, Phase 3.5).
// The probe hook itself is unit-tested in probe.dom.test.tsx; here the hook is
// stubbed so a test can hand FileView an observation and watch the pane react:
// a clean buffered pane follows and keeps its chosen mode, a pane without a
// buffer (the plain-mode Markdown fallback) replaces its loaded data, and a
// dirty pane shows the advisory with the diff-check action.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EditorView } from "@codemirror/view";
import { revisionOf } from "../editor/buffer.ts";
import { clearDirtyRegistryForTests } from "../editor/dirtyRegistry.ts";
import type { EditableFile, FileProbeResult } from "../editor/api.ts";
import type { ExternalChangeProbeOptions } from "../editor/probe.ts";

const PLAIN_MD = "# Title\n\nalpha\nbeta\ngamma\n";

let served: { content: string; editable: boolean } = { content: PLAIN_MD, editable: true };

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const { content, editable } = served;
    return {
      path: "repos/x/doc.md",
      size: content.length,
      binary: false,
      truncated: false,
      editable,
      editabilityReason: editable ? null : "read_only_root",
      content,
      ...(editable ? { revision: revisionOf(content) } : {}),
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));
vi.mock("../editor/api.ts", () => ({
  getEditableFile: vi.fn(),
  putFile: vi.fn(),
  probeFileMeta: vi.fn(),
}));
let probeOptions: ExternalChangeProbeOptions | null = null;
vi.mock("../editor/probe.ts", () => ({
  useExternalChangeProbe: (options: ExternalChangeProbeOptions) => {
    probeOptions = options;
  },
}));
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

const { FileView } = await import("./FileView.tsx");
const { getEditableFile } = await import("../editor/api.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(<FileView filePath="repos/x/doc.md" paneId="pane-1" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

function editableFile(content: string): EditableFile {
  return {
    path: "repos/x/doc.md",
    content,
    size: new TextEncoder().encode(content).byteLength,
    binary: false,
    truncated: false,
    editable: true,
    editabilityReason: null,
    revision: revisionOf(content),
  };
}

async function deliver(result: FileProbeResult): Promise<void> {
  await act(async () => {
    probeOptions!.onResult(result);
    await Promise.resolve();
  });
}

const pressed = () =>
  [...host.querySelectorAll('button[aria-pressed="true"]')].map((b) => b.textContent);
const editorView = () => EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
const status = () => host.querySelector(".file-editor-status")?.textContent ?? "";

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  clearDirtyRegistryForTests();
  vi.mocked(getEditableFile).mockReset();
  previewProps.length = 0;
  probeOptions = null;
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
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("clean pane auto-follow", () => {
  it("a read-only preview pane follows the disk and keeps its chosen mode", async () => {
    await render();
    expect(pressed()).toEqual(["Preview"]); // the buffered pane's view mode
    const updated = "# Title\n\nupdated externally\n";
    vi.mocked(getEditableFile).mockResolvedValue(editableFile(updated));

    await deliver({ kind: "revision", revision: revisionOf(updated) });

    expect(pressed()).toEqual(["Preview"]); // mode survived the data swap
    expect(previewProps.at(-1)?.source).toBe(updated);
    expect(editorView().state.doc.toString()).toBe(updated);
    expect(status()).toContain("Reloaded after an external change");
  });

  it("a pane without an editor buffer replaces its loaded data", async () => {
    // >300k chars: the plain-mode Markdown fallback — read-only, no buffer.
    const hugeLine = "x".repeat(60_000);
    const huge = (marker: string) =>
      `# Big\n\n${marker}\n${(hugeLine + "\n").repeat(6)}`;
    served = { content: huge("before"), editable: true };
    await render();
    expect(host.querySelector(".file-editor-shell")).toBeNull();
    expect(probeOptions!.path).toBe("repos/x/doc.md");

    const updated = huge("after");
    vi.mocked(getEditableFile).mockResolvedValue(editableFile(updated));
    await deliver({ kind: "revision", revision: revisionOf(updated) });

    expect(host.querySelector(".fb-plain")?.textContent).toContain("after");
    expect(status()).toContain("Reloaded after an external change");
  });
});

describe("dirty pane advisory", () => {
  it("keeps the buffer, announces the change, and offers the diff check", async () => {
    await render();
    await act(async () => {
      editorView().dispatch({ changes: { from: 0, insert: "mine " } });
    });
    const external = "# Title\n\nexternal\n";
    await deliver({ kind: "revision", revision: revisionOf(external) });

    expect(editorView().state.doc.toString()).toBe("mine " + PLAIN_MD);
    expect(status()).toContain("The file was changed outside this editor");
    const check = [...host.querySelectorAll(".file-external-note button")].at(0);
    expect(check?.textContent).toBe("Check diff");

    vi.mocked(getEditableFile).mockResolvedValue(editableFile(external));
    await act(async () => {
      (check as HTMLButtonElement).click();
      await Promise.resolve();
    });
    // The explicit action — not the probe — opened the ordinary conflict UI.
    expect(host.querySelector(".file-editor-resolution")).not.toBeNull();
  });
});
