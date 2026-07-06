import { useState } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { agentOf } from "../agents/registry.ts";
import { kindIcon, kindLabel } from "../lib/sessionkind.js";
import { sanitizeSeg } from "../lib/reponame.js";

// WorktreeModal: the worktree golden path. Instead of switching an existing working
// copy's branch out from under its running sessions (which the server now refuses),
// this spins a git worktree of the repo at ~/repos/<repo>@<branch> and starts a fresh
// session there — the parent (e.g. a main/develop 壁打ち clone) is left untouched.
//
// base is the start point; a new branch (optional) is created off it, else the
// worktree checks out the base branch itself. The folder name is derived server-side
// as <repo>@<sanitized-branch>; we preview it so the user sees where it lands.
const MODELS: [string, string][] = [
  ["", "既定"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

interface WorktreeModalProps {
  repo: string;
  branch?: string; // parent's current branch — the default base
  kinds: string[]; // available agent kinds (shell/ssm excluded, like LaunchModal)
  onClose: () => void;
  onCreate: (opts: { base: string; newBranch: string; kind: string; model: string }) => void;
}

export default function WorktreeModal({ repo, branch, kinds, onClose, onCreate }: WorktreeModalProps) {
  const [base, setBase] = useState(branch || "");
  const [newBranch, setNewBranch] = useState("");
  const [kind, setKind] = useState(kinds[0] || "claude");
  const [model, setModel] = useState("");
  const [busy, setBusy] = useState(false);

  const hasModel = agentOf(kind).caps.model;
  const target = (newBranch.trim() || base.trim());
  const folder = target ? `${repo}@${sanitizeSeg(target)}` : "";
  const canCreate = !busy && base.trim() !== "";

  const submit = () => {
    if (!canCreate) return;
    setBusy(true);
    onCreate({ base: base.trim(), newBranch: newBranch.trim(), kind, model: hasModel ? model : "" });
  };

  return (
    <Modal title={`worktree で作業を始める — ${repo}`} onClose={onClose} lockClose={busy}>
      <div className="modal-body">
        <div className="field-help">
          このブランチを <b>別ディレクトリの worktree</b> として開き、新しいセッションを起動します。
          元の作業コピーはそのままです。
        </div>

        <label className="pick-field">
          <span>基点ブランチ</span>
          <input value={base} onChange={(e) => setBase(e.target.value)} placeholder="main" autoFocus />
        </label>

        <label className="pick-field">
          <span>新規ブランチ（任意）</span>
          <input
            value={newBranch}
            onChange={(e) => setNewBranch(e.target.value)}
            placeholder={`${base.trim() || "基点"} から作成`}
          />
        </label>
        <div className="field-help">
          指定すると <code>{base.trim() || "基点"}</code> から新しいブランチを作成します。空なら基点ブランチを開きます。
        </div>

        <div className="field">
          <div className="field-label">エージェント</div>
          <div className="seg">
            {kinds.map((k) => (
              <button
                key={k}
                type="button"
                className={"seg-btn" + (kind === k ? " active" : "")}
                onClick={() => setKind(k)}
              >
                <Icon name={kindIcon(k)} /> {kindLabel(k)}
              </button>
            ))}
          </div>
        </div>

        {hasModel && (
          <div className="field">
            <div className="field-label">モデル</div>
            <div className="seg">
              {MODELS.map(([v, lbl]) => (
                <button
                  key={v}
                  type="button"
                  className={"seg-btn" + (model === v ? " active" : "")}
                  onClick={() => setModel(v)}
                >
                  {lbl}
                </button>
              ))}
            </div>
          </div>
        )}

        <div className="field-help">
          {folder ? (
            <>作業コピーは <code>{folder}</code> に作成されます。</>
          ) : (
            "基点ブランチを入力してください。"
          )}
        </div>

      </div>
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose} disabled={busy}>
          キャンセル
        </button>
        <button type="button" className="primary" onClick={submit} disabled={!canCreate}>
          worktree を作成して起動
        </button>
      </footer>
    </Modal>
  );
}
