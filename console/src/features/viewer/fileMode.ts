// fileMode — the File pane's display-mode state machine (docs/44 §1.1, §1.8).
//
// The pane has two layers that Phase 3 collapses into one piece of state: the
// pane's own `mode: "view" | "edit"`, and — for Markdown — the top-level
// edit/preview/split control plus the preview renderer. Markdown makes those
// three modes the ONLY top-level control and derives the pane mode from them.
// A fourth top-level mode (a read-only source view for editable Markdown) is
// deliberately not offered: the edit surface subsumes it (§1.8).
//
// Markdown that cannot be edited (too large, CRLF, a read-only root, or the
// viewer's plain fallback) keeps today's preview / source / slides UI. That is
// the same state space rather than a separate one: with `editable: false`,
// `md: "edit"` renders the read-only CodeView instead of CodeMirror and
// `split` is not offered, which reproduces the current control set exactly.

/** Markdown's top-level mode. `edit` is the source surface — CodeMirror when
 *  the file is editable, the read-only CodeView when it is not. */
export type MarkdownMode = "edit" | "preview" | "split";

/** Which renderer draws the preview side. `slides` is MarpView; `normal` is
 *  MarkdownView, which stays available for Marp decks too (§1.1). */
export type PreviewRenderer = "normal" | "slides";

/** 図（.drawio）の面。`figure` が同梱ビューアの描画、`source` が XML そのもの
 *  （編集できるなら CodeMirror、できないなら読み取り専用の CodeView）。 */
export type DiagramMode = "figure" | "source";

/** The pane's own layer, derived for Markdown and driven directly otherwise. */
export type PaneMode = "view" | "edit";

/** What the loaded file allows, derived by the caller from the GET response. */
export interface FileModeCaps {
  /** Markdown rendering assets apply (Markdown language and not the huge/plain fallback). */
  markdown: boolean;
  /** The document is a Marp deck. Evaluated with the same debounce as the preview (§1.1). */
  marp: boolean;
  /** A validated, PUT-able editor snapshot exists, so the buffer can be edited. */
  editable: boolean;
  /** drawio の図として開ける（docs/65）。Markdown と同時に立つことはない。 */
  diagram: boolean;
}

export type FileModeState =
  | { kind: "plain"; mode: PaneMode }
  | { kind: "markdown"; md: MarkdownMode; renderer: PreviewRenderer }
  | { kind: "diagram"; diagram: DiagramMode };

/** Which surfaces the pane renders. `editor` and `source` are mutually
 *  exclusive: the same slot is CodeMirror when editable, CodeView when not. */
export interface FileSurfaces {
  /** The CodeMirror editing surface is shown. */
  editor: boolean;
  /** The read-only CodeView source surface is shown. */
  source: boolean;
  /** A Markdown preview is shown with this renderer, or null when there is none. */
  preview: PreviewRenderer | null;
  /** Source and preview are shown side by side. */
  split: boolean;
  /** drawio の図（同梱ビューア）を表示している。 */
  diagram: boolean;
}

/** The mode a freshly opened file starts in.
 *
 *  A line citation opens the source surface so the referenced row exists; a
 *  Marp deck otherwise opens as slides, and other Markdown as a preview. This
 *  matches the pre-Phase-3 behaviour, with `source` renamed to `edit`.
 *
 *  `requested` is the opener's explicit choice ("編集で開く" from a file menu).
 *  It wins over the defaults above — the user named the surface they want — but
 *  never over what the document allows: a non-editable file ignores `edit` and
 *  falls back to the mode it would have opened in anyway. `view` needs no
 *  special case: it IS the default (preview for Markdown). */
export function initialFileMode(
  caps: FileModeCaps,
  options: { hasTargetLine?: boolean; requested?: PaneMode } = {},
): FileModeState {
  // 図はまず図として開く。ただし行を指してきた引用と「編集で開く」は、その行 /
  // その編集面が無ければ意味を成さないので XML ソース側に着地させる（§65.4）。
  if (caps.diagram) {
    const wantsSource = options.hasTargetLine || options.requested === "edit";
    return { kind: "diagram", diagram: wantsSource ? "source" : "figure" };
  }
  if (options.requested === "edit" && caps.editable) {
    return caps.markdown
      ? { kind: "markdown", md: "edit", renderer: "normal" }
      : { kind: "plain", mode: "edit" };
  }
  if (!caps.markdown) return { kind: "plain", mode: "view" };
  if (options.hasTargetLine) return { kind: "markdown", md: "edit", renderer: "normal" };
  return { kind: "markdown", md: "preview", renderer: caps.marp ? "slides" : "normal" };
}

/** The Markdown modes offered as buttons. `split` needs an edit surface, so a
 *  non-editable document keeps only the source/preview pair it has today. */
export function availableMarkdownModes(caps: FileModeCaps): MarkdownMode[] {
  return caps.editable ? ["edit", "preview", "split"] : ["edit", "preview"];
}

/** The preview renderers offered. Order is presentational and left to the UI. */
export function availableRenderers(caps: FileModeCaps): PreviewRenderer[] {
  return caps.marp ? ["normal", "slides"] : ["normal"];
}

/** The renderer actually used: `slides` requires a Marp deck. */
export function effectiveRenderer(state: FileModeState, caps: FileModeCaps): PreviewRenderer {
  if (state.kind !== "markdown") return "normal";
  return caps.marp ? state.renderer : "normal";
}

/** The pane-level mode. For Markdown it is derived from the top-level mode
 *  rather than driven by its own control (§1.1). */
export function paneModeOf(state: FileModeState, caps: FileModeCaps): PaneMode {
  if (state.kind === "diagram") return state.diagram === "source" && caps.editable ? "edit" : "view";
  if (state.kind === "plain") return state.mode === "edit" && caps.editable ? "edit" : "view";
  if (!caps.editable) return "view";
  return state.md === "edit" || state.md === "split" ? "edit" : "view";
}

export function surfacesFor(state: FileModeState, caps: FileModeCaps): FileSurfaces {
  const none: FileSurfaces = { editor: false, source: false, preview: null, split: false, diagram: false };
  if (state.kind === "diagram") {
    if (state.diagram === "figure") return { ...none, diagram: true };
    return { ...none, ...(caps.editable ? { editor: true } : { source: true }) };
  }
  if (state.kind === "plain") {
    return state.mode === "edit" && caps.editable
      ? { ...none, editor: true }
      : { ...none, source: true };
  }
  const renderer = effectiveRenderer(state, caps);
  // `split` needs an edit surface. A non-editable document is never offered it
  // (docs/44 §1.1 keeps that case on preview / source / slides), so a state that
  // still carries it — one render before reconcileFileMode clamps it, or a
  // direct call — degrades to the same preview that clamp lands on.
  if (state.md === "preview" || (state.md === "split" && !caps.editable)) {
    return { ...none, preview: renderer };
  }
  if (state.md === "split") return { ...none, editor: true, preview: renderer, split: true };
  return { ...none, ...(caps.editable ? { editor: true } : { source: true }) };
}

/** Identity of the set of surfaces on screen, ignoring which renderer draws the
 *  preview. Choosing a renderer is not choosing a surface, so anything that
 *  reacts to "the user moved to another surface" — focus, above all — must not
 *  be triggered by it (docs/44 §5). */
export function surfaceKey(surfaces: FileSurfaces): string {
  return [surfaces.editor, surfaces.source, !!surfaces.preview, surfaces.split, surfaces.diagram].join("|");
}

/** One button of the Markdown mode group (docs/44 §5: a button group with
 *  `aria-pressed`, not a tablist). */
export interface MarkdownModeControl {
  mode: MarkdownMode;
  pressed: boolean;
  /** True when `edit` is the read-only CodeView rather than the editor, which
   *  is what the label has to say. */
  readOnlySource: boolean;
}

/** 図の面のボタン群（図 / ソース）。Markdown の 3 モード群と同時には出ない。 */
export function diagramModeControls(
  state: FileModeState,
  caps: FileModeCaps,
): { mode: DiagramMode; pressed: boolean; readOnlySource: boolean }[] {
  if (state.kind !== "diagram") return [];
  return (["figure", "source"] as DiagramMode[]).map((mode) => ({
    mode,
    pressed: state.diagram === mode,
    readOnlySource: mode === "source" && !caps.editable,
  }));
}

/** 図の面を選ぶ。 */
export function withDiagramMode(state: FileModeState, diagram: DiagramMode, caps: FileModeCaps): FileModeState {
  if (!caps.diagram) return state;
  return { kind: "diagram", diagram };
}

export function markdownModeControls(
  state: FileModeState,
  caps: FileModeCaps,
): MarkdownModeControl[] {
  if (state.kind !== "markdown") return [];
  return availableMarkdownModes(caps).map((mode) => ({
    mode,
    pressed: state.md === mode,
    readOnlySource: mode === "edit" && !caps.editable,
  }));
}

/** One button of the preview renderer group. Empty when there is no renderer
 *  choice to make: no preview on screen, or not a Marp deck. */
export function rendererControls(
  state: FileModeState,
  caps: FileModeCaps,
): { renderer: PreviewRenderer; pressed: boolean }[] {
  const shown = surfacesFor(state, caps).preview;
  if (!shown || !caps.marp) return [];
  return availableRenderers(caps).map((renderer) => ({ renderer, pressed: shown === renderer }));
}

/** The ordered presentation states the Markdown mode command cycles through.
 *
 *  Modes are the outer axis and the renderer the inner one, so a Marp deck
 *  reaches both of its previews without adding a fourth top-level mode. For a
 *  read-only deck this is the same set of three states the pane offers today
 *  (source, preview, slides). */
export function fileModeCycle(caps: FileModeCaps): FileModeState[] {
  if (caps.diagram) {
    return [
      { kind: "diagram", diagram: "figure" },
      { kind: "diagram", diagram: "source" },
    ];
  }
  if (!caps.markdown) return [];
  const out: FileModeState[] = [];
  for (const md of availableMarkdownModes(caps)) {
    if (md === "edit") {
      out.push({ kind: "markdown", md, renderer: "normal" });
      continue;
    }
    for (const renderer of availableRenderers(caps)) out.push({ kind: "markdown", md, renderer });
  }
  return out;
}

/** Advance one step through {@link fileModeCycle}. A state outside the cycle
 *  (a stale renderer, a plain file) lands on the first entry. */
export function cycleFileMode(state: FileModeState, caps: FileModeCaps): FileModeState {
  const cycle = fileModeCycle(caps);
  if (cycle.length === 0) return state;
  if (caps.diagram) {
    return state.kind === "diagram" && state.diagram === "figure" ? cycle[1] : cycle[0];
  }
  const renderer = effectiveRenderer(state, caps);
  const at = cycle.findIndex(
    (entry) =>
      entry.kind === "markdown" &&
      state.kind === "markdown" &&
      entry.md === state.md &&
      // `edit` has no preview, so its renderer is not part of its identity.
      (entry.md === "edit" || entry.renderer === renderer),
  );
  return cycle[at === -1 ? 0 : (at + 1) % cycle.length];
}

/** Select a Markdown mode from the button group. */
export function withMarkdownMode(
  state: FileModeState,
  md: MarkdownMode,
  caps: FileModeCaps,
): FileModeState {
  if (!caps.markdown) return state;
  if (!availableMarkdownModes(caps).includes(md)) return state;
  const renderer = state.kind === "markdown" ? state.renderer : "normal";
  return { kind: "markdown", md, renderer };
}

/** Select a preview renderer. In `split` this applies to the right-hand side. */
export function withRenderer(
  state: FileModeState,
  renderer: PreviewRenderer,
  caps: FileModeCaps,
): FileModeState {
  if (state.kind !== "markdown") return state;
  if (!availableRenderers(caps).includes(renderer)) return state;
  return { ...state, renderer };
}

/** Select the pane mode of a non-Markdown file (the view/edit tablist). */
export function withPaneMode(
  state: FileModeState,
  mode: PaneMode,
  caps: FileModeCaps,
): FileModeState {
  if (state.kind !== "plain") return state;
  if (mode === "edit" && !caps.editable) return state;
  return { kind: "plain", mode };
}

/** Clamp a state whose capabilities changed underneath it.
 *
 *  Editing a Marp deck's front matter, or an external change that makes a file
 *  unsavable, can retract a mode or renderer while it is selected. Nothing here
 *  touches the buffer — it only keeps the selection inside what is offered. */
export function reconcileFileMode(state: FileModeState, caps: FileModeCaps): FileModeState {
  if (caps.diagram) return state.kind === "diagram" ? state : initialFileMode(caps);
  if (state.kind === "diagram") return initialFileMode(caps);
  if (state.kind === "plain") {
    if (!caps.markdown) return state.mode === "edit" && !caps.editable ? { kind: "plain", mode: "view" } : state;
    return initialFileMode(caps);
  }
  if (!caps.markdown) return { kind: "plain", mode: "view" };
  const renderer = effectiveRenderer(state, caps);
  // Losing the edit surface drops split to the preview rather than to the
  // read-only source: the rendered buffer stays on screen either way, and the
  // user did not ask to leave the preview.
  const md = availableMarkdownModes(caps).includes(state.md) ? state.md : "preview";
  return md === state.md && renderer === state.renderer ? state : { kind: "markdown", md, renderer };
}
