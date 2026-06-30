import { useEffect, useState } from "react";
import RepoPicker from "./RepoPicker.jsx";
import Modal from "./Modal.jsx";
import { deriveRepoName, sanitizeSeg, uniqueRepoName, repoNameRe } from "../lib/reponame.js";

// NewRepoModal: clone a repository into the workspace (~/repos) — a roomy dialog
// matching the New Session modal. Pick from a connected provider, or paste a URL.
// Submitting hands the clone to the parent (ReposSection) and closes immediately;
// progress shows as a spinner row in the left pane, so the user isn't trapped in a
// busy dialog while the clone runs.
const SOURCE_HELP = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

export default function NewRepoModal({ onClose, onClone, repos = [] }) {
  const [source, setSource] = useState("picker"); // 'picker' | 'url'
  const [sel, setSel] = useState(null); // picker: { cloneUrl, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [name, setName] = useState(""); // target folder (auto-filled on collision)
  const [nameEdited, setNameEdited] = useState(false);

  const cloneUrl = source === "picker" ? sel?.cloneUrl : url.trim();
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();

  // A working copy at the derived folder name already exists => the clone needs a
  // distinct folder, so offer a name (defaulting to "<repo>-<branch>").
  const derivedRepo = cloneUrl ? deriveRepoName(cloneUrl) : "";
  const repoNames = new Set(repos.map((r) => r.name));
  const collision = !!derivedRepo && repoNames.has(derivedRepo);
  const suggestedName = derivedRepo
    ? uniqueRepoName(`${derivedRepo}-${sanitizeSeg(cloneBranch)}`, repoNames)
    : "";

  useEffect(() => {
    if (!nameEdited) setName(suggestedName);
  }, [suggestedName, nameEdited]);

  const nameOk = !collision || (repoNameRe.test(name.trim()) && !repoNames.has(name.trim()));
  const canSubmit = !!cloneUrl && nameOk;

  const submit = (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    onClone({ remote_url: cloneUrl, branch: cloneBranch, name: collision ? name.trim() : "" });
    onClose();
  };

  return (
    <Modal title="リポジトリを clone" onClose={onClose} className="session-modal" as="form" onSubmit={submit}>
        <div className="modal-body">
          <div className="field">
            <div className="field-label">取得元</div>
            <div className="seg">
              {[
                ["picker", "接続から選ぶ"],
                ["url", "URL 手入力"],
              ].map(([v, label]) => (
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

          {/* 同名の作業コピーが既にある場合のみフォルダ名を尋ねる（別ブランチを並行 clone） */}
          {collision && (
            <div className="field">
              <div className="field-label">フォルダ名</div>
              <div className="field-help">
                作業コピー「{derivedRepo}」は既にあります。別の作業コピーとして clone するためフォルダ名を分けます。
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
