// Nothing but the state-to-display-text mappings of the editing surface. All branches, all
// pure functions that only look up tr(), so FileView just calls them.
//
// They live together because three places (the status line, the advisory note and the
// suggestion error) describe the same state in different vocabulary, and the wording is
// only comparable side by side.
import type { useT } from "../../../lib/i18n/index.ts";
import type { BufferValidationError } from "../../editor/buffer.ts";
import type { FileEditorModel } from "../../editor/model.ts";

type Tr = ReturnType<typeof useT>;

/** Body of the status line. The notice (most recent validation error, etc.) wins; phase is
 *  only consulted after it. */
export function editorStatusText(tr: Tr, model: FileEditorModel | null, notice: string): string {
  const phase = model?.phase;
  return (
    notice ||
    (phase === "dirty" && model?.message) ||
    (phase === "saving"
      ? tr("editor.status.saving")
      : phase === "saved"
        ? tr("editor.status.saved")
        : phase === "clean_risk_accepted"
          ? tr("editor.status.risk_accepted")
          : phase === "save_state_unknown"
            ? tr("editor.status.unknown")
            : phase === "conflict"
              ? tr("editor.status.conflict")
              : phase === "conflict_remote_unavailable"
                ? tr("editor.status.remote_unavailable")
                : model?.dirty
                  ? model.riskAccepted
                    ? tr("editor.status.dirty_risk")
                    : tr("editor.status.dirty")
                  : tr("editor.status.clean"))
  );
}

/** Whether the phase shows a resolution panel, i.e. turns the status line to the warning
 *  colour. */
export function isEditorAlert(phase: FileEditorModel["phase"] | undefined): boolean {
  return (
    phase === "save_state_unknown" ||
    phase === "conflict" ||
    phase === "conflict_remote_unavailable"
  );
}

// The probe's advisory (docs/log/44 §7.3): a polite status-line note, never an
// alert, and never a phase change. The resolution panels already speak for
// the alert phases, so the note yields to them.
export function externalNoteText(
  tr: Tr,
  observation: NonNullable<FileEditorModel["externalObservation"]> | null,
): string {
  if (!observation) return "";
  return observation.kind === "changed"
    ? tr("editor.external.changed")
    : observation.kind === "same_as_buffer"
      ? tr("editor.external.same_as_buffer")
      : observation.kind === "missing"
        ? tr("editor.external.missing")
        : observation.kind === "uneditable"
          ? tr("editor.external.uneditable")
          : tr("editor.external.boundary");
}

/** Buffer validation error code to display text. */
export function validationErrText(tr: Tr, code: BufferValidationError["code"]): string {
  const messages: Record<BufferValidationError["code"], string> = {
    too_large: tr("editor.validation.too_large"),
    binary_not_supported: tr("editor.validation.binary_not_supported"),
    unsupported_newline: tr("editor.validation.unsupported_newline"),
    invalid_unicode: tr("editor.validation.invalid_unicode"),
  };
  return messages[code];
}

// AI suggestion failure/rejection code to display text (docs/log/44 §3.4: `suggestion_stale`
// is a stable Console-side UI code, not an HTTP one). Codes from the buffer validator reuse
// the existing editor.validation.* messages.
export function suggestErrText(tr: Tr, code: string): string {
  switch (code) {
    case "suggestion_stale":
      return tr("editor.suggestion.stale");
    case "suggestion_invalid":
      return tr("editor.suggestion.invalid");
    case "selection_too_large":
      return tr("editor.suggestion.selection_too_large");
    case "instruction_invalid":
      return tr("editor.suggestion.instruction_invalid");
    case "io_timeout":
      return tr("editor.suggestion.timeout");
    case "too_large":
    case "binary_not_supported":
    case "unsupported_newline":
    case "invalid_unicode":
      return tr(`editor.validation.${code}`);
    default:
      return tr("editor.suggestion.failed");
  }
}
