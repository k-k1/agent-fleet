// AI suggestion panel (docs/log/44 Phase 4). One overlay holds all three stages: compose
// (entering the instruction), generating, and review (summary + a diff from the selection to
// the replacement + apply/discard). Unlike the conflict panel this is not an error, so it is
// not role="alert". Whether it can be applied is derived from baseRevision matching the
// current bufferRevision; a stale suggestion disables apply.
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { lineDiff } from "../DiffView.tsx";
import type { FileEditorModel } from "../../editor/model.ts";
import type { EditSuggestionEnvelope } from "../../editor/suggest.ts";

export interface EditorSuggestPanelProps {
  model: FileEditorModel;
  suggestion: EditSuggestionEnvelope | null;
  suggesting: boolean;
  instruction: string;
  onInstructionChange(value: string): void;
  onSubmit(): void;
  onAccept(): void;
  onReject(): void;
  onClose(): void;
}

export function EditorSuggestPanel(props: EditorSuggestPanelProps) {
  const tr = useT();
  const { model, suggestion } = props;
  if (suggestion) {
    const { range, replacement, summary } = suggestion.suggestion;
    const stale = suggestion.suggestion.baseRevision !== model.bufferRevision;
    const rows = stale ? [] : lineDiff(model.content.slice(range.from, range.to), replacement);
    return (
      <section className="file-editor-resolution file-editor-suggest" aria-label={tr("editor.suggestion.title")}>
        <h4>
          <Icon name="sparkle" /> {tr("editor.suggestion.title")}
        </h4>
        <p>{summary}</p>
        {stale ? (
          <p className="muted">{tr("editor.suggestion.stale")}</p>
        ) : (
          <div className="file-suggest-diff" aria-label={tr("editor.diff_aria")}>
            <pre>
              {rows.map((row, index) => (
                <span
                  key={index}
                  className={row.t === "del" ? "diff-mine" : row.t === "add" ? "diff-remote" : "diff-same"}
                >
                  {row.t === "del" ? "− " : row.t === "add" ? "+ " : "  "}
                  {row.text}
                  {"\n"}
                </span>
              ))}
            </pre>
          </div>
        )}
        <div className="file-editor-actions">
          <button type="button" autoFocus disabled={stale} onClick={props.onAccept}>
            {tr("editor.suggestion.apply")}
          </button>
          <button type="button" onClick={props.onReject}>
            {tr("editor.suggestion.reject")}
          </button>
          <button type="button" onClick={props.onClose}>
            {tr("editor.cancel")}
          </button>
        </div>
      </section>
    );
  }
  return (
    <section className="file-editor-resolution file-editor-suggest" aria-label={tr("editor.suggestion.title")}>
      <h4>
        <Icon name="sparkle" /> {tr("editor.suggestion.title")}
      </h4>
      <p className="muted">{tr("editor.suggestion.compose_hint")}</p>
      <textarea
        value={props.instruction}
        placeholder={tr("editor.suggestion.placeholder")}
        rows={3}
        autoFocus
        disabled={props.suggesting}
        onChange={(event) => props.onInstructionChange(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !props.suggesting) {
            event.preventDefault();
            props.onSubmit();
          }
        }}
      />
      <div className="file-editor-actions">
        <button
          type="button"
          disabled={props.suggesting || !props.instruction.trim()}
          onClick={props.onSubmit}
        >
          {props.suggesting ? (
            <>
              <Icon name="loading" spin /> {tr("editor.suggestion.generating")}
            </>
          ) : (
            tr("editor.suggestion.generate")
          )}
        </button>
        <button type="button" onClick={props.onClose}>
          {tr("editor.cancel")}
        </button>
      </div>
    </section>
  );
}
