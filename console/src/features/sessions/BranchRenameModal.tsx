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
import { useT } from "../../lib/i18n/index.ts";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useSettings } from "../../lib/settings.ts";

const PREFIXES = ["feat/", "fix/", "refactor/", "chore/", "docs/"];
// temp/ is the auto-generated placeholder prefix (deferred naming) — never offered
// as a chip, but a chip click should replace it just like a sibling prefix.
const STRIP_PREFIXES = [...PREFIXES, "temp/"];
const stripKnownPrefix = (v: string): string => {
  for (const p of STRIP_PREFIXES) if (v.startsWith(p)) return v.slice(p.length);
  return v;
};

interface BranchRenameModalProps {
  name: string; // session slug
  branch: string; // current branch (prefill)
  onClose: () => void;
  onSaved: () => void;
}

export function BranchRenameModal({ name, branch, onClose, onSaved }: BranchRenameModalProps) {
  const branchSuggest = useSettings().branchSuggestEnabled;
  const [value, setValue] = useState(branch);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
  const toast = useToast();
  const tr = useT();
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
      toast(tr("sx.branch_rename_failed"));
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
      toast(tr("sx.ai_suggest_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  // Toggle a prefix: prepend it (swapping any existing known/temp prefix), or strip
  // it when the name already has exactly that prefix.
  const togglePrefix = (p: string) =>
    setValue((v) => (v.startsWith(p) ? stripKnownPrefix(v) : p + stripKnownPrefix(v)));

  // Adopt the AI proposal (a bare kebab-case slug): keep whichever chip prefix the
  // current value carries, so adopting doesn't drop the selected type.
  const adoptProposal = () => {
    const cur = PREFIXES.find((p) => value.startsWith(p));
    setValue(cur ? cur + proposal : proposal);
    setProposal("");
  };

  return (
    <Modal title={tr("sx.branch_rename_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">{tr("sx.branch_label")}</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={tr("sx.branch_ph")}
            autoFocus
          />
          <span className="ui-field-hint">
            {tr("sx.branch_hint_pre")}<code>git branch -m</code>{tr("sx.branch_hint_post")}
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
        {/* 設定 > AI補助「ブランチ名の提案」。かつてはセッションのタイトル提案の
            設定に相乗りしていて、しかも off でもボタンは出ていた。 */}
        {branchSuggest && (
          <div>
            <Button icon={suggesting ? "loading" : "sparkle"} onClick={suggest} disabled={busy}>
              {tr("sx.ai_suggest")}
            </Button>
          </div>
        )}
        {proposal && (
          <div className="sm-proposal">
            <span className="sm-proposal-label">{tr("sx.proposal")}</span>
            <span className="sm-proposal-text">{proposal}</span>
            <Button
              small
              variant="primary"
              disabled={busy}
              onClick={adoptProposal}
            >
              {tr("sx.adopt")}
            </Button>
          </div>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={saving}>
          {tr("sx.save")}
        </Button>
      </footer>
    </Modal>
  );
}
