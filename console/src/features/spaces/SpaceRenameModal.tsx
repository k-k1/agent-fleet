// SpaceRenameModal — change a space's display name (ADR 0142 decision 3). Only the name shown
// in the Console changes: the folder under ~/spaces keeps its name, so the sessions in it and
// their transcripts stay where they are. An empty name shows the folder again.
import { NO_AUTOFILL } from "../../ui/noAutofill.ts";
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { renameSpace, spacesOwner } from "./store.ts";
import type { Space } from "./store.ts";

/** Mirrors spacex.NameMaxRunes on the Agent. */
export const SPACE_NAME_MAX = 100;

/** `owner` is the workspace the dialog was opened in (spacesOwner() then): the rename is only sent
 *  to it, and the dialog is closed by its parent when the workspace changes. */
// The Agent's refusals, worded here: their codes (bad_name, ...) are shared with other routes, so
// they are not put in the global err.* table.
const RENAME_ERRORS: Record<string, "pj.space_rename_bad_name" | "pj.space_rename_gone" | "pj.space_rename_changed"> = {
  bad_name: "pj.space_rename_bad_name",
  space_not_found: "pj.space_rename_gone",
  space_changed: "pj.space_rename_changed",
};

export function SpaceRenameModal({
  space,
  owner,
  onClose,
}: {
  space: Pick<Space, "folder" | "name">;
  owner: string;
  onClose: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [value, setValue] = useState(space.name);
  const [saving, setSaving] = useState(false);
  // The Agent counts characters (code points), not UTF-16 units, so a name of 100 characters with
  // astral ones is as valid as one without; an HTML maxLength would cut it.
  const tooLong = [...value.trim()].length > SPACE_NAME_MAX;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (saving || tooLong) return;
    setSaving(true);
    try {
      const res = await renameSpace(space.folder, value.trim(), owner);
      if (res?.stale) {
        onClose();
        return;
      }
      if (res?.error) {
        const err = res.error as Parameters<typeof errText>[0];
        const key = err && typeof err === "object" && err.code ? RENAME_ERRORS[err.code] : undefined;
        // Anything else (rename_failed: the Agent could not read or write the marker) keeps the
        // Agent's own text as the detail, under a heading in the member's language.
        toast(key ? tr(key) : tr("pj.space_rename_error", { detail: errText(err) }));
        return;
      }
      onClose();
    } catch {
      // A failure of a request made for another workspace says nothing about this one.
      if (spacesOwner() === owner) toast(tr("pj.space_rename_failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={tr("pj.space_rename_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">{tr("pj.space_rename_label")}</span>
          <input {...NO_AUTOFILL}
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={space.folder}
            // The dialog's focus trap focuses this and remembers the pencil that opened it, so
            // Esc and Save put the focus back there (a React autoFocus would take its place).
            data-autofocus
            aria-invalid={tooLong || undefined}
          />
          <span className="ui-field-hint">
            {tooLong ? tr("pj.space_rename_too_long", { max: SPACE_NAME_MAX }) : tr("pj.space_rename_hint", { folder: space.folder })}
          </span>
        </label>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={saving || tooLong}>
          {tr("sx.save")}
        </Button>
      </footer>
    </Modal>
  );
}
