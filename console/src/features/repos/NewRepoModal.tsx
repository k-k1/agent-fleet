// NewRepoModal — clone a repository into ~/repos. Source picking lives in the
// shared CloneForm (起動導線 Ph2); this dialog adds the clone-only extras — fork a
// new branch and pick a distinct folder name on demand. The clone itself runs in
// the parent (spinner row in the rail) so the user isn't trapped in a busy dialog.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { CloneForm } from "./CloneForm.tsx";
import type { CloneSource } from "./CloneForm.tsx";
import type { CloneRequest } from "./clone.ts";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../../lib/reponame.ts";
import { useT } from "../../lib/i18n/index.ts";

export type { CloneRequest };

interface NewRepoModalProps {
  onClose?: () => void;
  onClone: (req: CloneRequest) => void;
  repos?: { name: string }[];
}

export function NewRepoModal({ onClose, onClone, repos = [] }: NewRepoModalProps) {
  const tr = useT();
  const [src, setSrc] = useState<CloneSource>({ cloneUrl: "", branch: "" });
  const [newBranch, setNewBranch] = useState("");
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);

  const cloneUrl = src.cloneUrl;
  const cloneBranch = src.branch;
  const cloneNewBranch = cloneUrl ? newBranch.trim() : "";
  const targetBranch = cloneNewBranch || cloneBranch;

  // A distinct folder is needed when forking a new branch, or on a name collision.
  const derivedRepo = cloneUrl ? deriveRepoName(cloneUrl) : "";
  const repoNames = new Set(repos.map((r) => r.name));
  const collision = !!derivedRepo && repoNames.has(derivedRepo);
  const wantName = !!cloneNewBranch || collision;
  const suggestedName = derivedRepo
    ? uniqueRepoName(`${derivedRepo}-${sanitizeSeg(targetBranch)}`, repoNames)
    : "";

  useEffect(() => {
    if (!nameEdited) setName(suggestedName);
  }, [suggestedName, nameEdited]);

  const nameOk = !wantName || (repoNameRe.test(name.trim()) && !repoNames.has(name.trim()));
  const canSubmit = !!cloneUrl && nameOk;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    onClone({
      remote_url: cloneUrl,
      branch: cloneBranch,
      new_branch: cloneNewBranch,
      name: wantName ? name.trim() : "",
    });
    onClose?.();
  };

  return (
    <Modal title={tr("rp.clone_repo_title")} onClose={onClose} as="form" onSubmit={submit}>
      <div className="ui-modal-body">
        <CloneForm onChange={setSrc} />

        {!!cloneUrl && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("rp.new_branch_optional")}</span>
            <input
              value={newBranch}
              onChange={(e) => setNewBranch(e.target.value)}
              placeholder={tr("rp.create_from", { branch: cloneBranch || tr("rp.default_branch") })}
            />
            <span className="ui-field-hint">
              {tr("rp.new_branch_hint_pre")} <code>{cloneBranch || tr("rp.default_branch")}</code> {tr("rp.new_branch_hint_post")}
            </span>
          </div>
        )}

        {wantName && (
          <div className="ui-field">
            <span className="ui-field-label">{tr("rp.folder_name")}</span>
            <span className="ui-field-hint">
              {cloneNewBranch ? (
                <>
                  {tr("rp.workcopy_newbranch_pre")} <code>{cloneNewBranch}</code> {tr("rp.workcopy_newbranch_post")}
                </>
              ) : (
                tr("rp.workcopy_exists", { name: derivedRepo })
              )}
            </span>
            <input
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameEdited(true);
              }}
              placeholder={suggestedName}
            />
            {!nameOk && (
              <span className="ui-field-hint">{tr("rp.name_rule_hint")}</span>
            )}
          </div>
        )}
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          {tr("rp.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={!canSubmit}>
          {tr("rp.clone")}
        </Button>
      </footer>
    </Modal>
  );
}
