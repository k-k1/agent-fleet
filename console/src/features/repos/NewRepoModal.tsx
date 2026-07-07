// NewRepoModal — clone a repository into ~/repos. Provider picker or URL; the
// clone itself runs in the parent (spinner row in the rail) so the user isn't
// trapped in a busy dialog. Port of the old components/NewRepoModal.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { RepoPicker } from "./RepoPicker.tsx";
import type { RepoSelection } from "./RepoPicker.tsx";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../../lib/reponame.ts";

const SOURCE_HELP: Record<string, string> = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

export interface CloneRequest {
  remote_url: string;
  branch: string;
  name: string;
  new_branch: string;
}

interface NewRepoModalProps {
  onClose?: () => void;
  onClone: (req: CloneRequest) => void;
  repos?: { name: string }[];
}

export function NewRepoModal({ onClose, onClone, repos = [] }: NewRepoModalProps) {
  const [source, setSource] = useState<"picker" | "url">("picker");
  const [sel, setSel] = useState<RepoSelection | null>(null);
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [newBranch, setNewBranch] = useState("");
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);

  const cloneUrl = source === "picker" ? sel?.cloneUrl : url.trim();
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
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
      remote_url: cloneUrl || "",
      branch: cloneBranch,
      new_branch: cloneNewBranch,
      name: wantName ? name.trim() : "",
    });
    onClose?.();
  };

  return (
    <Modal title="リポジトリを clone" onClose={onClose} as="form" onSubmit={submit}>
      <div className="ui-modal-body">
        <div className="ui-field">
          <span className="ui-field-label">取得元</span>
          <div className="ui-seg">
            {(
              [
                ["picker", "接続から選ぶ"],
                ["url", "URL 手入力"],
              ] as const
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                className={"seg-btn" + (source === v ? " active" : "")}
                onClick={() => setSource(v)}
              >
                {label}
              </button>
            ))}
          </div>
          <span className="ui-field-hint">{SOURCE_HELP[source]}</span>

          {source === "picker" ? (
            <RepoPicker onChange={setSel} />
          ) : (
            <>
              <label className="ui-field">
                <span className="ui-field-label">clone URL</span>
                <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
              </label>
              <label className="ui-field">
                <span className="ui-field-label">ブランチ（任意）</span>
                <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="既定ブランチ" />
              </label>
            </>
          )}
        </div>

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
                  新規ブランチ <code>{cloneNewBranch}</code> の作業コピーを別フォルダへ clone します。
                </>
              ) : (
                <>作業コピー「{derivedRepo}」は既にあります。別の作業コピーとして clone するためフォルダ名を分けます。</>
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
          Clone
        </Button>
      </footer>
    </Modal>
  );
}
