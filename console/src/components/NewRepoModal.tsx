import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import RepoPicker from "./RepoPicker.jsx";
import type { RepoSelection } from "./RepoPicker.jsx";
import Modal from "./Modal.jsx";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../lib/reponame.js";

// NewRepoModal: clone a repository into the workspace (~/repos) — a roomy dialog
// matching the New Session modal. Pick from a connected provider, or paste a URL.
// Submitting hands the clone to the parent (ReposSection) and closes immediately;
// progress shows as a spinner row in the left pane, so the user isn't trapped in a
// busy dialog while the clone runs.
const SOURCE_HELP: Record<string, string> = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

interface CloneRequest {
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

export default function NewRepoModal({ onClose, onClone, repos = [] }: NewRepoModalProps) {
  const [source, setSource] = useState<"picker" | "url">("picker");
  const [sel, setSel] = useState<RepoSelection | null>(null); // picker: { cloneUrl, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [newBranch, setNewBranch] = useState(""); // optional: fork a fresh branch off the base
  const [name, setName] = useState(""); // target folder (auto-filled on collision / new branch)
  const [nameEdited, setNameEdited] = useState(false);

  const cloneUrl = source === "picker" ? sel?.cloneUrl : url.trim();
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim(); // base branch
  const cloneNewBranch = cloneUrl ? newBranch.trim() : ""; // fork this branch off the base
  const targetBranch = cloneNewBranch || cloneBranch; // branch the clone ends up on

  // The clone needs its own folder when forking a new branch (always) or when a
  // working copy already exists at the derived name. Default it to "<repo>-<branch>".
  const derivedRepo = cloneUrl ? deriveRepoName(cloneUrl) : "";
  const repoNames = new Set(repos.map((r) => r.name));
  const collision = !!derivedRepo && repoNames.has(derivedRepo);
  const wantName = !!cloneNewBranch || collision; // send an explicit folder name
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
    <Modal title="リポジトリを clone" onClose={onClose} className="session-modal" as="form" onSubmit={submit}>
        <div className="modal-body">
          <div className="field">
            <div className="field-label">取得元</div>
            <div className="seg">
              {([
                ["picker", "接続から選ぶ"],
                ["url", "URL 手入力"],
              ] as const).map(([v, label]) => (
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
            <div className="field-help">{SOURCE_HELP[source]}</div>

            {source === "picker" ? (
              <RepoPicker onChange={setSel} />
            ) : (
              <div className="stack">
                <label className="pick-field">
                  <span>clone URL</span>
                  <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://… / git@…" />
                </label>
                <label className="pick-field">
                  <span>ブランチ（任意）</span>
                  <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="既定ブランチ" />
                </label>
              </div>
            )}
          </div>

          {/* 新規ブランチ（任意）：指定ブランチを基点に新しいブランチを作成して clone */}
          {!!cloneUrl && (
            <div className="field">
              <div className="field-label">新規ブランチ（任意）</div>
              <label className="pick-field">
                <span>ブランチ名</span>
                <input
                  value={newBranch}
                  onChange={(e) => setNewBranch(e.target.value)}
                  placeholder={`${cloneBranch || "既定ブランチ"} から作成`}
                />
              </label>
              <div className="field-help">
                指定すると <code>{cloneBranch || "既定ブランチ"}</code> を基点に新しいブランチを作成して切り替えます。空なら基点ブランチのまま。
              </div>
            </div>
          )}

          {/* フォルダ名：新規ブランチを作る／同名の作業コピーが既にある場合に分ける */}
          {wantName && (
            <div className="field">
              <div className="field-label">フォルダ名</div>
              <div className="field-help">
                {cloneNewBranch
                  ? <>新規ブランチ <code>{cloneNewBranch}</code> の作業コピーを別フォルダへ clone します。</>
                  : <>作業コピー「{derivedRepo}」は既にあります。別の作業コピーとして clone するためフォルダ名を分けます。</>}
              </div>
              <label className="pick-field">
                <span>フォルダ名</span>
                <input
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value);
                    setNameEdited(true);
                  }}
                  placeholder={suggestedName}
                />
                {!nameOk && (
                  <span className="field-help">英数字始まりの一意な名前にしてください（既存の作業コピーと重複不可）。</span>
                )}
              </label>
            </div>
          )}
        </div>

        <footer className="modal-foot">
          <button type="button" className="ghost" onClick={onClose}>
            キャンセル
          </button>
          <button type="submit" className="primary" disabled={!canSubmit}>
            Clone
          </button>
        </footer>
    </Modal>
  );
}
