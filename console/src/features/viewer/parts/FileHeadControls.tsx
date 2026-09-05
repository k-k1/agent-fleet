// The small presentational pieces of FileView's info bar (ViewHead). Their only hook is
// useT; state and refs stay in FileView, so what lives here is the "what to show" branch and
// the markup, while "when it changes" belongs to the caller.
//
// The unit of splitting is the control group itself (docs/log/44 §1.1): the plain view/edit
// tab row, the diagram pair, the Markdown triple plus renderer, and the image pair are
// mutually exclusive — two of them must never be shown at once.
import type { KeyboardEvent, RefObject } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import FileIcon from "../../../ui/FileIcon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { baseName, documentLabel, humanSize, langLabel } from "../../../lib/filemeta.ts";
import { downloadURL } from "../../../core/api/client.ts";
import {
  diagramModeControls,
  markdownModeControls,
  type DiagramMode,
  type FileModeCaps,
  type FileModeState,
  type MarkdownMode,
  type PreviewRenderer,
} from "../fileMode.ts";
import type { DrawioState } from "../DrawioView.tsx";

/** Read-only heading: name, kind tag, size / line count. */
export function FileHeadMeta(props: {
  filePath: string;
  size?: number;
  truncated?: boolean;
  lfs?: boolean;
  imgFmt: string | null;
  isPdf: boolean;
  isDoc: boolean;
  isText: boolean;
  imgDims: { w: number; h: number } | null;
  pdfPages: number | null;
  lines: number;
  huge: boolean;
}) {
  const tr = useT();
  const { filePath, imgFmt, isPdf, isDoc, isText, imgDims, pdfPages } = props;
  const isImage = !!imgFmt;
  return (
    <>
      <span className="fi-name mono">
        <FileIcon name={baseName(filePath)} /> {baseName(filePath)}
      </span>
      {isImage ? (
        <span className="fi-tag">{imgFmt.toUpperCase()}</span>
      ) : isPdf ? (
        <span className="fi-tag">PDF</span>
      ) : isDoc ? (
        <span className="fi-tag">{documentLabel(filePath)}</span>
      ) : isText ? (
        <span className="fi-tag">{langLabel(filePath)}</span>
      ) : null}
      <span className="fi-meta muted">
        {humanSize(props.size)}
        {isImage && imgDims ? ` · ${imgDims.w}×${imgDims.h}` : ""}
        {isPdf && pdfPages != null ? tr("view.pdf.pages_meta", { n: pdfPages }) : ""}
        {!isImage && !isPdf && !isDoc && isText ? tr("view.lines_meta", { n: props.lines }) : ""}
        {props.truncated ? tr("view.head_only") : ""}
      </span>
      {props.huge && (
        <span className="fi-tag" title={tr("view.plain_mode_tip")}>
          {tr("view.plain_mode")}
        </span>
      )}
      {props.lfs && (
        <span className="fi-tag" title={tr("view.lfs_tip")}>
          {tr("view.lfs_pointer")}
        </span>
      )}
    </>
  );
}

/** Controls for an editable file: the view/edit tab row (plain only), save, AI suggest. */
export function FileEditControls(props: {
  showTabs: boolean;
  paneMode: "view" | "edit";
  viewTabRef: RefObject<HTMLButtonElement | null>;
  editTabRef: RefObject<HTMLButtonElement | null>;
  onModeKeyDown(event: KeyboardEvent<HTMLDivElement>): void;
  onChangeMode(next: "view" | "edit"): void;
  saveDisabled: boolean;
  saving: boolean;
  suggestDisabled: boolean;
  suggesting: boolean;
  /** Settings > AI assist, "File edit suggestions". When off the button is not rendered at
      all: never offer a control that can be pressed only to be refused (400
      feature_disabled). */
  suggestEnabled: boolean;
  onSave(): void;
  onSuggest(): void;
}) {
  const tr = useT();
  const { paneMode } = props;
  return (
    <>
      {/* Non-Markdown keeps the view/edit tablist. Markdown drives the same
          pane layer from its own top-level button group below, so the two
          control kinds never appear together (docs/log/44 §1.1, §5). */}
      {props.showTabs && (
        <div
          className="ui-seg sm file-mode-tabs"
          role="tablist"
          aria-label={tr("editor.mode_group")}
          onKeyDown={props.onModeKeyDown}
        >
          <button
            ref={props.viewTabRef}
            type="button"
            className={"seg-btn" + (paneMode === "view" ? " active" : "")}
            role="tab"
            aria-selected={paneMode === "view"}
            tabIndex={paneMode === "view" ? 0 : -1}
            onClick={() => props.onChangeMode("view")}
          >
            {tr("editor.mode.view")}
          </button>
          <button
            ref={props.editTabRef}
            type="button"
            className={"seg-btn" + (paneMode === "edit" ? " active" : "")}
            role="tab"
            aria-selected={paneMode === "edit"}
            tabIndex={paneMode === "edit" ? 0 : -1}
            onClick={() => props.onChangeMode("edit")}
          >
            {tr("editor.mode.edit")}
          </button>
        </div>
      )}
      <button
        type="button"
        className="file-save-btn"
        disabled={props.saveDisabled}
        onClick={props.onSave}
        title={tr("editor.save_tip")}
      >
        <Icon name={props.saving ? "loading" : "save"} spin={props.saving} /> {tr("editor.save")}
      </button>
      {props.suggestEnabled && (
        <button
          type="button"
          className="file-save-btn file-suggest-btn"
          disabled={props.suggestDisabled}
          onClick={props.onSuggest}
          title={tr("editor.suggestion.button_tip")}
        >
          <Icon name={props.suggesting ? "loading" : "sparkle"} spin={props.suggesting} />{" "}
          {tr("editor.suggestion.button")}
        </button>
      )}
    </>
  );
}

/** The diagram (drawio) pair, plus the page / zoom readout while a diagram is shown. */
export function FileDiagramControls(props: {
  fileMode: FileModeState;
  caps: FileModeCaps;
  modeGroupRef: RefObject<HTMLSpanElement | null>;
  onChangeMode(next: DiagramMode): void;
  showState: boolean;
  diagramState: DrawioState | null;
}) {
  const tr = useT();
  const { diagramState } = props;
  return (
    <>
      <span
        ref={props.modeGroupRef}
        className="ui-seg sm md-toggle"
        role="group"
        aria-label={tr("view.diagram_display_mode")}
      >
        {diagramModeControls(props.fileMode, props.caps).map((control) => (
          <button
            key={control.mode}
            type="button"
            aria-pressed={control.pressed}
            className={"seg-btn" + (control.pressed ? " active" : "")}
            onClick={() => props.onChangeMode(control.mode)}
          >
            {control.mode === "figure"
              ? tr("view.diagram")
              : // with no editing surface, call it the read-only Source tab instead
                control.readOnlySource
                ? tr("view.source")
                : tr("editor.mode.edit")}
          </button>
        ))}
      </span>
      {props.showState && diagramState && (
        <span className="fi-meta muted">
          {diagramState.pages > 1
            ? tr("view.diagram_page", { page: diagramState.page, pages: diagramState.pages })
            : ""}
          {diagramState.scale !== 1 ? ` ${Math.round(diagramState.scale * 100)}%` : ""}
        </span>
      )}
    </>
  );
}

/** The Markdown triple (preview / split / source-edit) and the renderer switch inside it. */
export function FileMarkdownControls(props: {
  fileMode: FileModeState;
  caps: FileModeCaps;
  modeGroupRef: RefObject<HTMLSpanElement | null>;
  onChangeMode(next: MarkdownMode): void;
  renderers: { renderer: PreviewRenderer; pressed: boolean }[];
  onChangeRenderer(next: PreviewRenderer): void;
}) {
  const tr = useT();
  return (
    <>
      <span
        ref={props.modeGroupRef}
        className="ui-seg sm md-toggle"
        role="group"
        aria-label={tr("view.markdown_display_mode")}
      >
        {markdownModeControls(props.fileMode, props.caps).map((control) => (
          <button
            key={control.mode}
            type="button"
            aria-pressed={control.pressed}
            className={"seg-btn" + (control.pressed ? " active" : "")}
            onClick={() => props.onChangeMode(control.mode)}
          >
            {control.mode === "split"
              ? tr("editor.mode.split")
              : control.mode === "preview"
                ? tr("view.preview")
                : // Without an edit surface this is the read-only source
                  // view the pane has always offered — say so.
                  control.readOnlySource
                  ? tr("view.source")
                  : tr("editor.mode.edit")}
          </button>
        ))}
      </span>
      {props.renderers.length > 0 && (
        <span className="ui-seg sm md-toggle" role="group" aria-label={tr("editor.renderer_group")}>
          {props.renderers.map((control) => (
            <button
              key={control.renderer}
              type="button"
              aria-pressed={control.pressed}
              className={"seg-btn" + (control.pressed ? " active" : "")}
              onClick={() => props.onChangeRenderer(control.renderer)}
            >
              {control.renderer === "slides" ? tr("view.slides") : tr("editor.renderer.doc")}
            </button>
          ))}
        </span>
      )}
    </>
  );
}

/** Preview / source switch for images that are also text (SVG and the like). */
export function FileImageModeToggle(props: {
  mode: "preview" | "source";
  onChange(next: "preview" | "source"): void;
}) {
  const tr = useT();
  const { mode } = props;
  return (
    <span className="ui-seg sm md-toggle" role="group" aria-label={tr("view.image_display_mode")}>
      <button type="button" aria-pressed={mode === "preview"} className={"seg-btn" + (mode === "preview" ? " active" : "")} onClick={() => props.onChange("preview")}>
        {tr("view.preview")}
      </button>
      <button type="button" aria-pressed={mode === "source"} className={"seg-btn" + (mode === "source" ? " active" : "")} onClick={() => props.onChange("source")}>
        {tr("view.source")}
      </button>
    </span>
  );
}

/** Button that opens the reader view (docs/log/24). */
export function FileReaderButton({ onOpen }: { onOpen(): void }) {
  const tr = useT();
  return (
    <span className="ui-seg sm md-toggle">
      <button type="button" className="seg-btn" onClick={onOpen} title={tr("view.open_reader_tip")}>
        <Icon name="book" /> {tr("view.read_aloud")}
      </button>
    </span>
  );
}

// One flex child so path + download wrap (fileinfo flex-wrap) together —
// never the lone download icon on its own line.
export function FileHeadPath({ filePath }: { filePath: string }) {
  const tr = useT();
  return (
    <span className="fi-end">
      <span className="fi-path muted" title={filePath}>
        {filePath}
      </span>
      <a className="fi-dl" href={downloadURL(filePath)} download={baseName(filePath)} title={tr("view.download")}>
        <Icon name="cloud-download" />
      </a>
    </span>
  );
}
