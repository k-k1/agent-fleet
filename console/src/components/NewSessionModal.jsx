import { useState } from "react";
import { apiJSON } from "../api.js";
import RepoPicker from "./RepoPicker.jsx";

// NewSessionModal: a clear, roomy dialog for creating a session. Three steps —
// 種類 (claude/shell), リポジトリ (provider picker / URL / none), セッション名 —
// each labelled with help text, instead of the cramped inline form.
const lastSeg = (full) =>
  (full.split("/").pop() || "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 40);

const SOURCE_HELP = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
  none: "リポジトリを clone せず、ホーム(~) でそのまま起動します。",
};

export default function NewSessionModal({ onClose, onCreated }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState("claude");
  const [source, setSource] = useState("picker"); // 'picker' | 'url' | 'none'
  const [sel, setSel] = useState(null); // picker: { cloneUrl, fullName, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [dir, setDir] = useState("");
  const [busy, setBusy] = useState(false);

  const onPick = (s) => {
    setSel(s);
    if (s && !name.trim()) setName(lastSeg(s.fullName));
  };

  const cloneUrl = source === "picker" ? sel?.cloneUrl : source === "url" ? url.trim() : "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const cloning = source !== "none" && !!cloneUrl;

  const sourceOk = source === "none" || !!cloneUrl;
  const canSubmit = !!name.trim() && sourceOk && !busy;

  const submit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/sessions", "POST", {
        name: name.trim(),
        kind,
        dir: source === "none" ? dir.trim() : "",
        remote_url: cloning ? cloneUrl : "",
        branch: cloning ? cloneBranch : "",
      });
      if (res && res.error) {
        alert("作成に失敗: " + (res.error.message || res.error));
        return;
      }
      onCreated(name.trim(), cloning);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={busy ? undefined : onClose}>
      <form className="modal session-modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <header className="modal-head">
          <h3 className="modal-title">新しいセッション</h3>
          <button type="button" className="icon" title="閉じる" onClick={onClose}>
            ✕
          </button>
        </header>

        <div className="modal-body">
          {/* 種類 */}
          <div className="field">
            <div className="field-label">種類</div>
            <div className="seg big">
              <button
                type="button"
                className={"seg-btn" + (kind === "claude" ? " active" : "")}
                onClick={() => setKind("claude")}
              >
                claude
                <span className="seg-sub">Claude Code を起動</span>
              </button>
              <button
                type="button"
                className={"seg-btn" + (kind === "shell" ? " active" : "")}
                onClick={() => setKind("shell")}
              >
                shell
                <span className="seg-sub">通常のシェル (bash)</span>
              </button>
            </div>
          </div>

          {/* リポジトリ */}
          <div className="field">
            <div className="field-label">リポジトリ</div>
            <div className="seg">
              {[
                ["picker", "接続から選ぶ"],
                ["url", "URL 手入力"],
                ["none", "リポなし"],
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

            {source === "picker" && <RepoPicker onChange={onPick} />}
            {source === "url" && (
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
            {source === "none" && (
              <label className="pick-field">
                <span>ディレクトリ</span>
                <input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="既定 ~（ホーム）" />
              </label>
            )}
          </div>

          {/* 名前 */}
          <div className="field">
            <div className="field-label">セッション名</div>
            <input
              className="name-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例: my-app（英数・_・- ）"
            />
            <div className="field-help">一覧に表示される識別名。リポジトリを選ぶと自動入力されます。</div>
          </div>
        </div>

        <footer className="modal-foot">
          <button type="button" className="ghost" onClick={onClose} disabled={busy}>
            キャンセル
          </button>
          <button type="submit" className="primary" disabled={!canSubmit}>
            {busy ? (cloning ? "Cloning…" : "作成中…") : "作成して開く"}
          </button>
        </footer>
      </form>
    </div>
  );
}
