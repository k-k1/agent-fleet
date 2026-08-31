import { describe, expect, it } from "vitest";
import {
  availableMarkdownModes,
  availableRenderers,
  cycleFileMode,
  diagramModeControls,
  effectiveRenderer,
  fileModeCycle,
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
  type FileModeCaps,
  type FileModeState,
} from "./fileMode.ts";

const caps = (over: Partial<FileModeCaps> = {}): FileModeCaps => ({
  markdown: true,
  marp: false,
  editable: true,
  diagram: false,
  ...over,
});

const md = (state: Partial<{ md: "edit" | "preview" | "split"; renderer: "normal" | "slides" }> = {}): FileModeState => ({
  kind: "markdown",
  md: state.md ?? "preview",
  renderer: state.renderer ?? "normal",
});

describe("initialFileMode", () => {
  it("opens plain text in view", () => {
    expect(initialFileMode(caps({ markdown: false }))).toEqual({ kind: "plain", mode: "view" });
  });

  it("opens markdown in preview and a Marp deck in slides", () => {
    expect(initialFileMode(caps())).toEqual(md({ md: "preview", renderer: "normal" }));
    expect(initialFileMode(caps({ marp: true }))).toEqual(md({ md: "preview", renderer: "slides" }));
  });

  it("opens the source surface for a line citation", () => {
    // The pre-Phase-3 pane opened `source` so the cited row exists; `edit` is
    // that surface now, for a Marp deck too.
    expect(initialFileMode(caps(), { hasTargetLine: true })).toEqual(md({ md: "edit" }));
    expect(initialFileMode(caps({ marp: true }), { hasTargetLine: true })).toEqual(md({ md: "edit" }));
  });

  it("opens the edit surface when the opener asked for it (「編集」で開く)", () => {
    expect(initialFileMode(caps({ markdown: false }), { requested: "edit" })).toEqual({ kind: "plain", mode: "edit" });
    // Markdown's edit surface is its source mode, and it wins over the deck's
    // usual slides opening — the user named the surface.
    expect(initialFileMode(caps(), { requested: "edit" })).toEqual(md({ md: "edit" }));
    expect(initialFileMode(caps({ marp: true }), { requested: "edit" })).toEqual(md({ md: "edit" }));
  });

  it("ignores an edit request the document cannot honour", () => {
    expect(initialFileMode(caps({ markdown: false, editable: false }), { requested: "edit" })).toEqual({
      kind: "plain",
      mode: "view",
    });
    expect(initialFileMode(caps({ editable: false }), { requested: "edit" })).toEqual(md({ md: "preview" }));
  });

  it("treats a view request as the default opening", () => {
    expect(initialFileMode(caps({ markdown: false }), { requested: "view" })).toEqual({ kind: "plain", mode: "view" });
    expect(initialFileMode(caps({ marp: true }), { requested: "view" })).toEqual(md({ renderer: "slides" }));
  });
});

describe("offered controls", () => {
  it("offers split only with an edit surface", () => {
    expect(availableMarkdownModes(caps())).toEqual(["edit", "preview", "split"]);
    expect(availableMarkdownModes(caps({ editable: false }))).toEqual(["edit", "preview"]);
  });

  it("offers the slides renderer only for a Marp deck", () => {
    expect(availableRenderers(caps())).toEqual(["normal"]);
    expect(availableRenderers(caps({ marp: true }))).toEqual(["normal", "slides"]);
  });

  it("keeps a non-editable Marp deck on today's three controls", () => {
    // slides + preview + source, i.e. the current pane, expressed as the same
    // state space rather than a separate legacy one.
    const readOnlyMarp = caps({ marp: true, editable: false });
    expect(availableMarkdownModes(readOnlyMarp)).toEqual(["edit", "preview"]);
    expect(fileModeCycle(readOnlyMarp)).toHaveLength(3);
  });
});

describe("effectiveRenderer", () => {
  it("falls back to the normal preview when the document is not Marp", () => {
    expect(effectiveRenderer(md({ renderer: "slides" }), caps())).toBe("normal");
    expect(effectiveRenderer(md({ renderer: "slides" }), caps({ marp: true }))).toBe("slides");
  });
});

describe("paneModeOf", () => {
  it("derives the pane mode from the markdown mode", () => {
    expect(paneModeOf(md({ md: "edit" }), caps())).toBe("edit");
    expect(paneModeOf(md({ md: "split" }), caps())).toBe("edit");
    expect(paneModeOf(md({ md: "preview" }), caps())).toBe("view");
  });

  it("never reports edit without an edit surface", () => {
    const readOnly = caps({ editable: false });
    expect(paneModeOf(md({ md: "edit" }), readOnly)).toBe("view");
    expect(paneModeOf(md({ md: "split" }), readOnly)).toBe("view");
    expect(paneModeOf({ kind: "plain", mode: "edit" }, readOnly)).toBe("view");
  });

  it("drives the pane mode directly for plain text", () => {
    expect(paneModeOf({ kind: "plain", mode: "edit" }, caps({ markdown: false }))).toBe("edit");
    expect(paneModeOf({ kind: "plain", mode: "view" }, caps({ markdown: false }))).toBe("view");
  });
});

describe("surfacesFor", () => {
  it("maps plain text onto the editor or the read-only source", () => {
    expect(surfacesFor({ kind: "plain", mode: "edit" }, caps({ markdown: false }))).toEqual({
      editor: true,
      source: false,
      preview: null,
      split: false,
      diagram: false,
    });
    expect(surfacesFor({ kind: "plain", mode: "view" }, caps({ markdown: false }))).toEqual({
      editor: false,
      source: true,
      preview: null,
      split: false,
      diagram: false,
    });
  });

  it("renders the markdown source surface as CodeMirror or CodeView", () => {
    expect(surfacesFor(md({ md: "edit" }), caps())).toMatchObject({ editor: true, source: false });
    expect(surfacesFor(md({ md: "edit" }), caps({ editable: false }))).toMatchObject({
      editor: false,
      source: true,
    });
  });

  it("puts both surfaces on screen in split", () => {
    expect(surfacesFor(md({ md: "split" }), caps())).toMatchObject({
      editor: true,
      source: false,
      preview: "normal",
      split: true,
      diagram: false,
    });
    // A non-editable document is never offered split. A state that still carries
    // it must render what reconcileFileMode clamps it to — a plain preview — and
    // never a split the contract does not allow (docs/log/44 §1.1).
    expect(surfacesFor(md({ md: "split" }), caps({ editable: false }))).toEqual({
      editor: false,
      source: false,
      preview: "normal",
      split: false,
      diagram: false,
    });
    expect(surfacesFor(md({ md: "split" }), caps({ editable: false }))).toEqual(
      surfacesFor(reconcileFileMode(md({ md: "split" }), caps({ editable: false })), caps({ editable: false })),
    );
  });

  it("renders the selected renderer in preview and clamps a stale one", () => {
    expect(surfacesFor(md({ md: "preview", renderer: "slides" }), caps({ marp: true })).preview).toBe("slides");
    expect(surfacesFor(md({ md: "preview", renderer: "slides" }), caps()).preview).toBe("normal");
  });
});

describe("control groups", () => {
  it("marks the pressed mode and labels edit as the read-only source", () => {
    expect(markdownModeControls(md({ md: "preview" }), caps())).toEqual([
      { mode: "edit", pressed: false, readOnlySource: false },
      { mode: "preview", pressed: true, readOnlySource: false },
      { mode: "split", pressed: false, readOnlySource: false },
    ]);
    expect(markdownModeControls(md({ md: "edit" }), caps({ editable: false }))).toEqual([
      { mode: "edit", pressed: true, readOnlySource: true },
      { mode: "preview", pressed: false, readOnlySource: false },
    ]);
  });

  it("has no mode group for a non-Markdown file", () => {
    expect(markdownModeControls({ kind: "plain", mode: "view" }, caps({ markdown: false }))).toEqual([]);
  });

  it("offers renderers only with a preview on screen and a Marp deck", () => {
    const marp = caps({ marp: true });
    expect(rendererControls(md({ md: "preview" }), marp)).toEqual([
      { renderer: "normal", pressed: true },
      { renderer: "slides", pressed: false },
    ]);
    expect(rendererControls(md({ md: "split", renderer: "slides" }), marp)).toEqual([
      { renderer: "normal", pressed: false },
      { renderer: "slides", pressed: true },
    ]);
    // No preview on screen, and nothing to choose between on a plain document.
    expect(rendererControls(md({ md: "edit" }), marp)).toEqual([]);
    expect(rendererControls(md({ md: "preview" }), caps())).toEqual([]);
  });

  it("marks the renderer actually in use when the selection went stale", () => {
    // The deck's front matter lost `marp: true` while slides was selected: the
    // group disappears rather than showing a pressed button that does nothing.
    expect(rendererControls(md({ md: "preview", renderer: "slides" }), caps())).toEqual([]);
  });
});

describe("surfaceKey", () => {
  it("ignores which renderer draws the preview", () => {
    // Choosing a renderer is not moving to another surface, so it must not read
    // as one (docs/log/44 §5 — focus follows the mode).
    const marp = caps({ marp: true });
    expect(surfaceKey(surfacesFor(md({ md: "split", renderer: "normal" }), marp))).toBe(
      surfaceKey(surfacesFor(md({ md: "split", renderer: "slides" }), marp)),
    );
    expect(surfaceKey(surfacesFor(md({ md: "preview", renderer: "normal" }), marp))).toBe(
      surfaceKey(surfacesFor(md({ md: "preview", renderer: "slides" }), marp)),
    );
  });

  it("separates every move between surfaces", () => {
    const c = caps();
    const keys = (["edit", "preview", "split"] as const).map((m) =>
      surfaceKey(surfacesFor(md({ md: m }), c)),
    );
    expect(new Set(keys).size).toBe(3);
    // The read-only source is a different surface from the editor.
    expect(surfaceKey(surfacesFor(md({ md: "edit" }), caps({ editable: false })))).not.toBe(keys[0]);
  });
});

describe("cycleFileMode", () => {
  it("cycles the three modes of an editable non-Marp document", () => {
    const c = caps();
    let state = md({ md: "edit" });
    const seen: string[] = [];
    for (let i = 0; i < 3; i++) {
      state = cycleFileMode(state, c);
      seen.push(state.kind === "markdown" ? state.md : state.kind);
    }
    expect(seen).toEqual(["preview", "split", "edit"]);
  });

  it("reaches both previews of a Marp deck without a fourth mode", () => {
    const c = caps({ marp: true });
    const cycle = fileModeCycle(c);
    expect(cycle).toEqual([
      md({ md: "edit" }),
      md({ md: "preview", renderer: "normal" }),
      md({ md: "preview", renderer: "slides" }),
      md({ md: "split", renderer: "normal" }),
      md({ md: "split", renderer: "slides" }),
    ]);
    // Every entry is reachable and the walk returns to where it started.
    let state: FileModeState = cycle[0];
    for (let i = 0; i < cycle.length; i++) state = cycleFileMode(state, c);
    expect(state).toEqual(cycle[0]);
  });

  it("covers exactly today's source / preview / slides on a read-only deck", () => {
    const c = caps({ marp: true, editable: false });
    expect(fileModeCycle(c)).toEqual([
      md({ md: "edit" }),
      md({ md: "preview", renderer: "normal" }),
      md({ md: "preview", renderer: "slides" }),
    ]);
  });

  it("ignores the renderer while on the source surface", () => {
    // `edit` shows no preview, so a stale renderer must not push the walk off
    // the cycle and reset it to the first entry.
    const c = caps({ marp: true });
    expect(cycleFileMode(md({ md: "edit", renderer: "slides" }), c)).toEqual(
      md({ md: "preview", renderer: "normal" }),
    );
  });

  it("lands on the first entry from a state outside the cycle", () => {
    const c = caps({ editable: false });
    expect(cycleFileMode(md({ md: "split" }), c)).toEqual(md({ md: "edit" }));
  });

  it("leaves a plain file alone", () => {
    const state: FileModeState = { kind: "plain", mode: "view" };
    expect(cycleFileMode(state, caps({ markdown: false }))).toBe(state);
  });
});

describe("explicit selection", () => {
  it("keeps the renderer across a mode change", () => {
    const c = caps({ marp: true });
    expect(withMarkdownMode(md({ md: "preview", renderer: "slides" }), "split", c)).toEqual(
      md({ md: "split", renderer: "slides" }),
    );
  });

  it("refuses a mode or renderer that is not offered", () => {
    const readOnly = caps({ editable: false });
    const state = md({ md: "preview" });
    expect(withMarkdownMode(state, "split", readOnly)).toBe(state);
    expect(withRenderer(state, "slides", caps())).toBe(state);
    expect(withRenderer(state, "slides", caps({ marp: true }))).toEqual(
      md({ md: "preview", renderer: "slides" }),
    );
  });

  it("switches the pane mode only for plain text with an edit surface", () => {
    const plain: FileModeState = { kind: "plain", mode: "view" };
    expect(withPaneMode(plain, "edit", caps({ markdown: false }))).toEqual({ kind: "plain", mode: "edit" });
    expect(withPaneMode(plain, "edit", caps({ markdown: false, editable: false }))).toBe(plain);
    const markdown = md({ md: "preview" });
    expect(withPaneMode(markdown, "edit", caps())).toBe(markdown);
  });
});

describe("reconcileFileMode", () => {
  it("drops a stale slides renderer when the deck stops being Marp", () => {
    expect(reconcileFileMode(md({ md: "preview", renderer: "slides" }), caps())).toEqual(
      md({ md: "preview", renderer: "normal" }),
    );
  });

  it("leaves an unaffected state identical", () => {
    const state = md({ md: "split" });
    expect(reconcileFileMode(state, caps())).toBe(state);
  });

  it("drops split to preview when the edit surface goes away", () => {
    expect(reconcileFileMode(md({ md: "split" }), caps({ editable: false }))).toEqual(
      md({ md: "preview" }),
    );
  });

  it("keeps the source surface when the edit surface goes away", () => {
    // `edit` stays offered without an edit surface — it becomes the read-only
    // CodeView, which is where a non-editable Markdown file belongs.
    expect(reconcileFileMode(md({ md: "edit" }), caps({ editable: false }))).toEqual(md({ md: "edit" }));
  });

  it("switches state kind when the file stops or starts being markdown", () => {
    expect(reconcileFileMode(md({ md: "split" }), caps({ markdown: false }))).toEqual({
      kind: "plain",
      mode: "view",
    });
    expect(reconcileFileMode({ kind: "plain", mode: "edit" }, caps({ marp: true }))).toEqual(
      md({ md: "preview", renderer: "slides" }),
    );
  });

  it("drops a plain file out of edit when it stops being editable", () => {
    expect(reconcileFileMode({ kind: "plain", mode: "edit" }, caps({ markdown: false, editable: false }))).toEqual({
      kind: "plain",
      mode: "view",
    });
  });
});

// ── 図（.drawio）の面 — docs/log/65 §65.4 ─────────────────────────────────────
describe("diagram mode", () => {
  const dcaps = (over: Partial<FileModeCaps> = {}) => caps({ markdown: false, diagram: true, ...over });

  it("図として開き、行を指した引用と「編集で開く」はソースに着地する", () => {
    expect(initialFileMode(dcaps())).toEqual({ kind: "diagram", diagram: "figure" });
    expect(initialFileMode(dcaps(), { hasTargetLine: true })).toEqual({ kind: "diagram", diagram: "source" });
    expect(initialFileMode(dcaps(), { requested: "edit" })).toEqual({ kind: "diagram", diagram: "source" });
  });

  it("図の面はペインの view 層、ソース面は編集できるときだけ edit 層", () => {
    expect(paneModeOf({ kind: "diagram", diagram: "figure" }, dcaps())).toBe("view");
    expect(paneModeOf({ kind: "diagram", diagram: "source" }, dcaps())).toBe("edit");
    expect(paneModeOf({ kind: "diagram", diagram: "source" }, dcaps({ editable: false }))).toBe("view");
  });

  it("ソース面は編集できないときだけ読み取り専用の CodeView になる", () => {
    expect(surfacesFor({ kind: "diagram", diagram: "figure" }, dcaps())).toEqual({
      editor: false,
      source: false,
      preview: null,
      split: false,
      diagram: true,
    });
    expect(surfacesFor({ kind: "diagram", diagram: "source" }, dcaps())).toMatchObject({ editor: true, diagram: false });
    expect(surfacesFor({ kind: "diagram", diagram: "source" }, dcaps({ editable: false }))).toMatchObject({
      source: true,
      editor: false,
    });
  });

  it("図とソースを 2 状態で往復する（キーボードコマンド）", () => {
    const cycle = fileModeCycle(dcaps());
    expect(cycle).toEqual([
      { kind: "diagram", diagram: "figure" },
      { kind: "diagram", diagram: "source" },
    ]);
    expect(cycleFileMode({ kind: "diagram", diagram: "figure" }, dcaps())).toEqual({ kind: "diagram", diagram: "source" });
    expect(cycleFileMode({ kind: "diagram", diagram: "source" }, dcaps())).toEqual({ kind: "diagram", diagram: "figure" });
    // 別種の状態から入ってきても図に着地する（clamp と同じ向き）。
    expect(cycleFileMode({ kind: "plain", mode: "view" }, dcaps())).toEqual({ kind: "diagram", diagram: "figure" });
  });

  it("能力が変わったら状態を寄せ直す（Markdown の面と混ざらない）", () => {
    expect(reconcileFileMode({ kind: "markdown", md: "preview", renderer: "normal" }, dcaps())).toEqual({
      kind: "diagram",
      diagram: "figure",
    });
    expect(reconcileFileMode({ kind: "diagram", diagram: "figure" }, caps({ markdown: false, diagram: false }))).toEqual({
      kind: "plain",
      mode: "view",
    });
  });

  it("図のときは Markdown の 3 モード群を出さない", () => {
    expect(markdownModeControls({ kind: "diagram", diagram: "figure" }, dcaps())).toEqual([]);
    expect(diagramModeControls({ kind: "diagram", diagram: "figure" }, dcaps())).toEqual([
      { mode: "figure", pressed: true, readOnlySource: false },
      { mode: "source", pressed: false, readOnlySource: false },
    ]);
    expect(diagramModeControls({ kind: "markdown", md: "preview", renderer: "normal" }, dcaps())).toEqual([]);
  });

  it("withDiagramMode は図でないファイルでは何もしない", () => {
    expect(withDiagramMode({ kind: "diagram", diagram: "figure" }, "source", dcaps())).toEqual({
      kind: "diagram",
      diagram: "source",
    });
    const plain: FileModeState = { kind: "plain", mode: "view" };
    expect(withDiagramMode(plain, "figure", caps({ markdown: false, diagram: false }))).toBe(plain);
  });

  it("surfaceKey は図の面を別の面として数える（焦点の移動判定）", () => {
    const figure = surfaceKey(surfacesFor({ kind: "diagram", diagram: "figure" }, dcaps()));
    const source = surfaceKey(surfacesFor({ kind: "diagram", diagram: "source" }, dcaps()));
    expect(figure).not.toBe(source);
  });
});
