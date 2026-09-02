// 編集面まわりの「状態 → 表示文言」だけを集めたもの。分岐しか無く、どれも
// tr() を引くだけの純関数なので、FileView からは呼ぶだけになる。
//
// ここに集めた理由は、同じ状態を 3 か所（状態行・advisory の注記・提案の
// エラー）が別々の語彙で言い分けていて、並べないと言い分けの一貫性が
// 見えないため。
import type { useT } from "../../../lib/i18n/index.ts";
import type { BufferValidationError } from "../../editor/buffer.ts";
import type { FileEditorModel } from "../../editor/model.ts";

type Tr = ReturnType<typeof useT>;

/** 状態行の本文。notice（直近の検証エラー等）が最優先で、次に phase を読む。 */
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

/** 解決パネルを出す（＝状態行を警告色にする）phase かどうか。 */
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

/** バッファ検証エラーのコード → 表示文言。 */
export function validationErrText(tr: Tr, code: BufferValidationError["code"]): string {
  const messages: Record<BufferValidationError["code"], string> = {
    too_large: tr("editor.validation.too_large"),
    binary_not_supported: tr("editor.validation.binary_not_supported"),
    unsupported_newline: tr("editor.validation.unsupported_newline"),
    invalid_unicode: tr("editor.validation.invalid_unicode"),
  };
  return messages[code];
}

// AI 提案の失敗/棄却コード → 表示文言（docs/log/44 §3.4: `suggestion_stale` は
// HTTP ではなく Console 側の安定 UI code）。buffer validator のコードは既存の
// editor.validation.* を再利用する。
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
