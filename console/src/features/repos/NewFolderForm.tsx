// NewFolderForm — the folder-name field for 新規フォルダ（取り込み元なし）, shared by
// the rail's リポジトリを追加 dialog (NewRepoModal) and the はじめる hub's new-folder
// stage (StartModal). One field, but two callers and one validation rule, and the
// rule is the server's: repoNameRe mirrors workspace/agent/git.go, and a name that
// already exists is refused there with 409 exists rather than overwritten.
import { useEffect, useRef } from "react";
import { repoNameRe } from "../../lib/reponame.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useT } from "../../lib/i18n/index.ts";

/** Whether `name` can be created: server charset + not an existing working copy. */
export const newFolderNameOk = (name: string, taken: Set<string>): boolean => {
  const n = name.trim();
  return repoNameRe.test(n) && !taken.has(n);
};

interface NewFolderFormProps {
  value: string;
  onChange: (v: string) => void;
  /** Existing working-copy names — a collision is a hard stop, not a suffix. */
  taken: Set<string>;
  /** Enter in the field submits (the hub stage has no other field to move to). */
  onSubmit?: () => void;
  /** Take focus on mount. Skipped on touch devices, where it would raise the soft
   * keyboard over the dialog (same rule as LaunchModal). */
  autoFocus?: boolean;
}

export function NewFolderForm({ value, onChange, taken, onSubmit, autoFocus = false }: NewFolderFormProps) {
  const tr = useT();
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    // preventScroll: the browser would otherwise scroll the field into view and push
    // the dialog's first line off screen (LaunchModal hit exactly this).
    if (autoFocus && !coarsePointer()) ref.current?.focus({ preventScroll: true });
  }, [autoFocus]);
  const trimmed = value.trim();
  const taken_ = !!trimmed && taken.has(trimmed);
  return (
    <div className="ui-field">
      <span className="ui-field-label">{tr("rp.folder_name")}</span>
      <input
        ref={ref}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Enter" || e.nativeEvent.isComposing || !onSubmit) return;
          e.preventDefault();
          onSubmit();
        }}
        placeholder={tr("rp.new_folder_ph")}
        aria-label={tr("rp.folder_name")}
      />
      <span className="ui-field-hint">{tr("rp.new_folder_hint")}</span>
      {!!trimmed && !newFolderNameOk(value, taken) && (
        <span className="ui-field-hint warn">{tr(taken_ ? "rp.new_folder_taken" : "rp.name_rule_hint")}</span>
      )}
    </div>
  );
}
