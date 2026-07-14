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

export type { CloneRequest };

interface NewRepoModalProps {
  onClose?: () => void;
  onClone: (req: CloneRequest) => void;
  repos?: { name: string }[];
}

export function NewRepoModal({ onClose, onClone, repos = [] }: NewRepoModalProps) {
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
    <Modal title="リポジトリをクローン" onClose={onClose} as="form" onSubmit={submit}>
      <div className="ui-modal-body">
        <CloneForm onChange={setSrc} />

        {!!cloneUrl && (
          <div className="ui-field">
            <span className="ui-field-label">新規ブランチ（任意）</span>
            <input
              value={newBranch}
              onChange={(e) => setNewBranch(e.target.value)}
              placeholder={`${cloneBranch || "既定ブランチ"} から作成`}
            />
            <span className="ui-field-hint">
              指定すると <code>{cloneBranch || "既定ブランチ"}</code> を基点に新しいブランチを作成して切り替えます。空なら基点ブランチのまま。
            </span>
          </div>
        )}

        {wantName && (
          <div className="ui-field">
            <span className="ui-field-label">フォルダ名</span>
            <span className="ui-field-hint">
              {cloneNewBranch ? (
                <>
                  新規ブランチ <code>{cloneNewBranch}</code> の作業コピーを別フォルダへクローンします。
                </>
              ) : (
                <>作業コピー「{derivedRepo}」は既にあります。別の作業コピーとしてクローンするためフォルダ名を分けます。</>
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
              <span className="ui-field-hint">英数字始まりの一意な名前にしてください（既存の作業コピーと重複不可）。</span>
            )}
          </div>
        )}
      </div>

      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose}>
          キャンセル
        </Button>
        <Button variant="primary" type="submit" disabled={!canSubmit}>
          クローン
        </Button>
      </footer>
    </Modal>
  );
}
