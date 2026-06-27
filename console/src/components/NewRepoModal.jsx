import { useState } from "react";
import { apiJSON } from "../api.js";
import RepoPicker from "./RepoPicker.jsx";

// NewRepoModal: clone a repository into the workspace (~/repos) — a roomy dialog
// matching the New Session modal. Pick from a connected provider, or paste a URL.
const SOURCE_HELP = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
};

export default function NewRepoModal({ onClose, onCloned }) {
  const [source, setSource] = useState("picker"); // 'picker' | 'url'
  const [sel, setSel] = useState(null); // picker: { cloneUrl, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false);

  const cloneUrl = source === "picker" ? sel?.cloneUrl : url.trim();
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const canSubmit = !!cloneUrl && !busy;

  const submit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/repos", "POST", { remote_url: cloneUrl, branch: cloneBranch });
      if (res && res.error) {
        alert("clone に失敗: " + (res.error.message || res.error));
        return;
      }
      onCloned();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onClose}>
      <form className="modal session-modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <header className="modal-head">
          <h3 className="modal-title">リポジトリを clone</h3>
          <button type="button" className="icon" title="閉じる" onClick={onClose}>
            ✕
          </button>
        </header>

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
        </div>

        <footer className="modal-foot">
          <button type="button" className="ghost" onClick={onClose} disabled={busy}>
            キャンセル
          </button>
          <button type="submit" className="primary" disabled={!canSubmit}>
            {busy ? "Cloning…" : "Clone"}
          </button>
        </footer>
      </form>
    </div>
  );
}
