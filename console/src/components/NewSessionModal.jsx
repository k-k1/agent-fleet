import { useEffect, useState } from "react";
import { api, apiJSON } from "../api.js";
import RepoPicker from "./RepoPicker.jsx";

// NewSessionModal: a clear, roomy dialog for creating a session.
// shell is the left / default kind — a one-click shell needs no repo, no dir, and
// an auto-filled name. claude additionally offers a model and a repo source
// (provider picker / clone URL / none).
const lastSeg = (full) =>
  (full.split("/").pop() || "").replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 40);

// uniqueName returns base, or base-2 / base-3 … when already taken.
const uniqueName = (base, taken) => {
  if (!taken.has(base)) return base;
  for (let i = 2; i < 1000; i++) {
    const n = `${base}-${i}`;
    if (!taken.has(n)) return n;
  }
  return base;
};

const SOURCE_HELP = {
  picker: "接続済みの GitHub / Bitbucket からリポジトリとブランチを選んで clone します。",
  url: "clone URL を手入力します（接続していないリポジトリ向け）。",
  none: "リポジトリを clone せず、ホーム(~) でそのまま起動します。",
};

export default function NewSessionModal({ onClose, onCreated }) {
  const [name, setName] = useState("");
  const [nameEdited, setNameEdited] = useState(false);
  const [kind, setKind] = useState("shell"); // shell is the default (left) kind
  const [model, setModel] = useState(""); // "" = claude default
  const [source, setSource] = useState("picker"); // 'picker' | 'url' | 'none'
  const [sel, setSel] = useState(null); // picker: { cloneUrl, fullName, branch }
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [dir, setDir] = useState("");
  const [taken, setTaken] = useState(new Set());
  const [busy, setBusy] = useState(false);

  // Existing session names, for auto-naming uniqueness.
  useEffect(() => {
    let alive = true;
    api("api/sessions")
      .then((d) => alive && setTaken(new Set((d.sessions || []).map((s) => s.name))))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  // Auto-fill the name (until the user types their own). claude with a chosen repo
  // names after the repo; otherwise the name is the kind word.
  const base = kind === "claude" && sel ? lastSeg(sel.fullName) : kind;
  useEffect(() => {
    if (!nameEdited) setName(uniqueName(base, taken));
  }, [base, taken, nameEdited]);

  const onPick = (s) => setSel(s);

  const isClaude = kind === "claude";
  const cloneUrl = !isClaude ? "" : source === "picker" ? sel?.cloneUrl : source === "url" ? url.trim() : "";
  const cloneBranch = source === "picker" ? sel?.branch || "" : branch.trim();
  const cloning = isClaude && source !== "none" && !!cloneUrl;

  // shell never needs a repo; claude's repo is optional (none is allowed).
  const sourceOk = !isClaude || source === "none" || !!cloneUrl;
  const canSubmit = !!name.trim() && sourceOk && !busy;

  const submit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/sessions", "POST", {
        name: name.trim(),
        kind,
        model: isClaude ? model : "",
        dir: isClaude && source === "none" ? dir.trim() : "",
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
          {/* 種類 — shell 左 / 既定 */}
          <div className="field">
            <div className="field-label">種類</div>
            <div className="seg big">
              <button
                type="button"
                className={"seg-btn" + (kind === "shell" ? " active" : "")}
                onClick={() => setKind("shell")}
              >
                shell
                <span className="seg-sub">通常のシェル (bash)</span>
              </button>
              <button
                type="button"
                className={"seg-btn" + (kind === "claude" ? " active" : "")}
                onClick={() => setKind("claude")}
              >
                claude
                <span className="seg-sub">Claude Code を起動</span>
              </button>
            </div>
          </div>

          {/* モデル + リポジトリ（claude のみ） */}
          {isClaude && (
            <>
              <div className="field">
                <div className="field-label">モデル</div>
                <div className="seg">
                  {[
                    ["", "既定"],
                    ["opus", "Opus"],
                    ["sonnet", "Sonnet"],
                    ["haiku", "Haiku"],
                  ].map(([v, label]) => (
                    <button
                      key={v || "default"}
                      type="button"
                      className={"seg-btn" + (model === v ? " active" : "")}
                      onClick={() => setModel(v)}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>

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
            </>
          )}

          {/* 名前 */}
          <div className="field">
            <div className="field-label">セッション名</div>
            <input
              className="name-input"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setNameEdited(true);
              }}
              placeholder="例: my-app（英数・_・- ）"
            />
            <div className="field-help">一覧に表示される識別名。自動入力済み（編集可）。</div>
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
