// BranchRenameModal — rename a worktree SESSION's branch (⋯ → ブランチ名を変更).
// Session-scoped so the AI suggestion summarizes THIS session's conversation.
// Save runs `git branch -m` on the session's working copy (folder = session id
// untouched); every session in that dir has its start branch follow, so the
// rename isn't read as drift. Conventional-commit prefixes offered as chips.
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { apiJSON, errText } from "../../core/api/client.ts";

const PREFIXES = ["feat/", "fix/", "refactor/", "chore/", "docs/"];
const stripKnownPrefix = (v: string): string => {
  for (const p of PREFIXES) if (v.startsWith(p)) return v.slice(p.length);
  return v;
};

interface BranchRenameModalProps {
  name: string; // session slug
  branch: string; // current branch (prefill)
  onClose: () => void;
  onSaved: () => void;
}

export function BranchRenameModal({ name, branch, onClose, onSaved }: BranchRenameModalProps) {
  const [value, setValue] = useState(branch);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
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
      if (typeof j.branch === "string" && j.branch) setProposal(j.branch);
    } catch {
      toast("AI 提案の取得に失敗しました（通信エラー）");
    } finally {
      setSuggesting(false);
    }
  };

  // Toggle a prefix: prepend it (swapping any existing known prefix), or strip it
  // when the name already has exactly that prefix.
  const togglePrefix = (p: string) =>
    setValue((v) => (v.startsWith(p) ? stripKnownPrefix(v) : p + stripKnownPrefix(v)));

  return (
    <Modal title="ブランチ名を変更" onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">ブランチ名</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder="例: feat/login-redirect"
            autoFocus
          />
          <span className="ui-field-hint">
            この worktree のブランチを <code>git branch -m</code> で改名します（フォルダ＝セッションはそのまま）。
          </span>
        </label>
        <div className="sm-prefix-row">
          {PREFIXES.map((p) => (
            <button
              key={p}
              type="button"
              className={"sm-prefix-chip" + (value.startsWith(p) ? " active" : "")}
              onClick={() => togglePrefix(p)}
              disabled={busy}
            >
              {p}
            </button>
          ))}
        </div>
        <div>
          <Button icon={suggesting ? "loading" : "sparkle"} onClick={suggest} disabled={busy}>
            AIに提案してもらう
          </Button>
        </div>
        {proposal && (
          <div className="sm-proposal">
            <span className="sm-proposal-label">提案</span>
            <span className="sm-proposal-text">{proposal}</span>
            <Button
              small
              variant="primary"
              disabled={busy}
              onClick={() => {
                setValue(proposal);
                setProposal("");
              }}
            >
              この案にする
            </Button>
          </div>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          キャンセル
        </Button>
        <Button variant="primary" type="submit" disabled={saving}>
          保存
        </Button>
      </footer>
    </Modal>
  );
}
