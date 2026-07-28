// Render tests for the File pane's Phase 3 modes (docs/44 §1.1, §1.8).
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
import { revisionOf } from "../editor/buffer.ts";

const MARP = "---\nmarp: true\n---\n\n# Deck\n\nbody\n";
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
// The renderers themselves are covered by their own suites; here they only need
// to say which one the pane picked.
vi.mock("./MarpView.tsx", () => ({ MarpView: () => <div data-surface="slides" /> }));
vi.mock("./MarkdownView.tsx", () => ({ MarkdownView: () => <div data-surface="preview" /> }));

const { FileView } = await import("./FileView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(props: { targetLine?: number }): Promise<void> {
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

beforeEach(() => {
  served = { content: PLAIN_MD, editable: true };
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
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
    // cited row was never drawn (docs/44 §1.8).
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
