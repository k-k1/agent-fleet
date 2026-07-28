// Focus hand-off between FileView and the editing surface (docs/44 §5).
//
// The editor mounts a commit after the pane's controls appear, so a mode can be
// chosen while CodeMirror is still coming up. That window cannot be observed
// with the real editor — `act` flushes React to quiescence, so the surface is
// always ready by the time a test can click. Here CodeEditor is a stub whose
// `onReady` the test fires by hand, which is exactly the seam the contract is
// about: FileView must honour a focus request the editor was not yet able to
// serve, and must not honour one the user has since walked away from.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { revisionOf } from "../editor/buffer.ts";

const MD = "# Title\n\nalpha\n";

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    return {
      path: "repos/x/doc.md",
      size: MD.length,
      binary: false,
      truncated: false,
      editable: true,
      editabilityReason: null,
      content: MD,
      revision: revisionOf(MD),
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));
vi.mock("./MarpView.tsx", () => ({ MarpView: () => <div data-surface="slides" /> }));
vi.mock("./MarkdownView.tsx", () => ({ MarkdownView: () => <div data-surface="preview" /> }));

// A stand-in for the editor that never reports ready on its own.
let latestOnReady: ((focus: () => void) => void) | null = null;
vi.mock("../editor/CodeEditor.tsx", () => ({
  CodeEditor: (props: { onReady?: (focus: () => void) => void }) => {
    latestOnReady = props.onReady ?? null;
    return (
      <div className="file-editor-cm">
        <div className="cm-content" tabIndex={-1} data-stub-editor />
      </div>
    );
  },
}));

const { FileView } = await import("./FileView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

const modeButton = (label: string) =>
  [...host.querySelectorAll('[aria-label="Markdown display mode"] button')].find(
    (b) => b.textContent === label,
  ) as HTMLButtonElement;

const stubContent = () => host.querySelector("[data-stub-editor]") as HTMLElement;

/** What CodeMirror does once its view exists. */
async function reportReady(): Promise<void> {
  const onReady = latestOnReady;
  await act(async () => {
    onReady?.(() => stubContent().focus());
  });
  await act(async () => {
    await Promise.resolve();
  });
}

async function settle(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(async () => {
  latestOnReady = null;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<FileView filePath="repos/x/doc.md" paneId="pane-1" />);
  });
  await settle();
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("a mode chosen before the editor is ready", () => {
  it("is honoured once the editor reports ready", async () => {
    // Regression: the request was consumed in a single microtask. With the
    // editor still mounting the call hit a null ref, the request was marked
    // spent, and nothing moved focus when the editor did come up.
    expect(document.activeElement).not.toBe(stubContent());
    await act(async () => modeButton("Edit").click());
    await settle();
    expect(document.activeElement).not.toBe(stubContent());

    await reportReady();
    expect(document.activeElement).toBe(stubContent());
  });

  it("is dropped when the user leaves the surface before it is ready", async () => {
    await act(async () => modeButton("Edit").click());
    await settle();
    await act(async () => modeButton("Preview").click());
    await settle();

    await reportReady();
    expect(document.activeElement).not.toBe(stubContent());
  });

  it("is served immediately when the editor was ready all along", async () => {
    await reportReady();
    await act(async () => modeButton("Split").click());
    await settle();
    expect(document.activeElement).toBe(stubContent());
  });

  it("is not raised at all by merely opening the file", async () => {
    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();
    await reportReady();
    expect(document.activeElement).toBe(outside);
    outside.remove();
  });
});
