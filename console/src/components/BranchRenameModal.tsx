import { useState } from "react";
import type { FormEvent } from "react";
import Modal from "./Modal.jsx";
import Icon from "./Icon.jsx";
import { apiJSON, errText } from "../api.js";
import { useToast } from "./ToastProvider.jsx";

// BranchRenameModal: rename the branch of a worktree SESSION (right-click → ブランチ名を
// 変更). Session-scoped so "AIに提案してもらう" summarizes THIS session's conversation —
// unambiguous even when several sessions share the worktree. Save runs git branch -m on
// the session's working copy (the folder, hence the session id, is left untouched); the
// start branch of every session in that dir follows, so it isn't read as drift.
interface BranchRenameModalProps {
  name: string; // session slug
  branch: string; // current branch (prefill)
  onClose: () => void;
  onSaved: () => void;
}

export default function BranchRenameModal({ name, branch, onClose, onSaved }: BranchRenameModalProps) {
  const [value, setValue] = useState(branch);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState(""); // AI candidate awaiting the user's OK
  const toast = useToast();
  const busy = saving || suggesting;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const next = value.trim();
    if (!next || next === branch) {
      onClose();
      return;
    }
    setSaving(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/rename-branch`, "POST", { name: next });
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      onSaved();
      onClose();
    } catch {
      toast("ブランチ名の変更に失敗しました（通信エラー）");
    } finally {
      setSaving(false);
    }
  };

  const suggest = async () => {
    if (busy) return;
    setSuggesting(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/suggest-branch`, "POST", {});
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      if (typeof j.branch === "string" && j.branch) {
        // Show it as a proposal — don't clobber what the user may be editing.
        setProposal(j.branch);
      }
    } catch {
      toast("AI 提案の取得に失敗しました（通信エラー）");
    } finally {
      setSuggesting(false);
    }
  };

  const applyProposal = () => {
    setValue(proposal);
    setProposal("");
  };

  return (
    <Modal
      title="ブランチ名を変更"
      onClose={onClose}
      className="session-modal title-modal"
      as="form"
      onSubmit={submit}
      lockClose={saving}
    >
      <div className="modal-body">
        <div className="field">
          <div className="field-label">ブランチ名</div>
          <input
            type="text"
            className="ti-input"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder="例: feat/login-redirect"
            autoFocus
          />
          <div className="field-help">
            この worktree のブランチを <code>git branch -m</code> で改名します（フォルダ＝セッションはそのまま）。
          </div>
        </div>

        <div className="ti-suggest-row">
          <button type="button" className="ti-suggest-btn" onClick={suggest} disabled={busy}>
            <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} /> AIに提案してもらう
          </button>
        </div>

        {proposal && (
          <div className="ti-proposal">
            <span className="ti-proposal-label">提案</span>
            <span className="ti-proposal-text">{proposal}</span>
            <button type="button" className="primary ti-proposal-apply" onClick={applyProposal} disabled={busy}>
              この案にする
            </button>
          </div>
        )}
      </div>
      <footer className="modal-foot">
        <button type="button" className="ghost" onClick={onClose} disabled={saving}>
          キャンセル
        </button>
        <button type="submit" className="primary" disabled={saving}>
          保存
        </button>
      </footer>
    </Modal>
  );
}
