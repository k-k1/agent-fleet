// SpaceRenameModal — change a space's display name (ADR 0142 decision 3). Only the name shown
// in the Console changes: the folder under ~/spaces keeps its name, so the sessions in it and
// their transcripts stay where they are. An empty name shows the folder again.
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText } from "../../core/api/client.ts";
import { renameSpace } from "./store.ts";
import type { Space } from "./store.ts";

/** Mirrors spacex.NameMaxRunes on the Agent. */
export const SPACE_NAME_MAX = 100;

export function SpaceRenameModal({ space, onClose }: { space: Pick<Space, "folder" | "name">; onClose: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [value, setValue] = useState(space.name);
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    try {
      const res = await renameSpace(space.folder, value.trim());
      if (res?.error) {
        toast(errText(res.error as Parameters<typeof errText>[0]));
        return;
      }
      onClose();
    } catch {
      toast(tr("pj.space_rename_failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={tr("pj.space_rename_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">{tr("pj.space_rename_label")}</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={space.folder}
            maxLength={SPACE_NAME_MAX}
            autoFocus
          />
          <span className="ui-field-hint">{tr("pj.space_rename_hint", { folder: space.folder })}</span>
        </label>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={saving}>
          {tr("sx.save")}
        </Button>
      </footer>
    </Modal>
  );
}
