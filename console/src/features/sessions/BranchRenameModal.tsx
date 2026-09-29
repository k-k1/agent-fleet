// BranchRenameModal — rename a worktree SESSION's branch (⋯ → "rename branch").
// Session-scoped so the AI suggestion summarizes THIS session's conversation.
// Save runs `git branch -m` on the session's working copy (folder = session id
// untouched); every session in that dir has its start branch follow, so the
// rename isn't read as drift. The chips are the prefixes the branch-name resolver gives this
// repository (ADR 0103 decision 8), or a fixed conventional-commit set from an older Agent.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useSettings } from "../../lib/settings.ts";
import { Icon } from "../../ui/Icon.tsx";
import { fetchBranchName, fetchBranchRule, warningText } from "../repos/branchRule.ts";
import { useBranchCheck } from "../repos/useLaunchBranchName.ts";

// The chips an Agent without the resolver gets.
const FALLBACK_PREFIXES = ["feat/", "fix/", "refactor/", "chore/", "docs/"];

/** Chips from the resolved kinds: each non-empty prefix once, in the resolver's order. */
export function chipPrefixes(kinds: { prefix: string }[] | null | undefined): string[] {
  if (!kinds) return FALLBACK_PREFIXES;
  const out: string[] = [];
  for (const k of kinds) if (k.prefix && !out.includes(k.prefix)) out.push(k.prefix);
  return out;
}

/** Strips whichever known prefix the name starts with. temp/ (deferred naming) is never a chip
 * but a chip click replaces it like a sibling prefix, and so do the fallback chips, which a
 * name from before the rules may still carry. Longest first, so fix/ never wins over fixup/. */
export function stripKnownPrefix(v: string, chips: string[]): string {
  const known = [...new Set([...chips, ...FALLBACK_PREFIXES, "temp/"])].sort((a, b) => b.length - a.length);
  for (const p of known) if (v.startsWith(p)) return v.slice(p.length);
  return v;
}

interface BranchRenameModalProps {
  name: string; // session slug
  branch: string; // current branch (prefill)
  /** The session's working copy under ~/repos, which the resolver routes are keyed by. "" leaves
   * the fixed chips and the bare AI slug. */
  repo?: string;
  onClose: () => void;
  onSaved: () => void;
}

export function BranchRenameModal({ name, branch, repo = "", onClose, onSaved }: BranchRenameModalProps) {
  const branchSuggest = useSettings().branchSuggestEnabled;
  const [value, setValue] = useState(branch);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
  // Whether the proposal is a whole name the resolver composed (prefix included) or a bare slug
  // from an Agent without the resolver, which keeps the chip prefix in front of it.
  const [proposalWhole, setProposalWhole] = useState(false);
  const [chips, setChips] = useState<string[]>(FALLBACK_PREFIXES);
  const warnings = useBranchCheck(repo, value === branch ? "" : value);
  useEffect(() => {
    if (!repo) return;
    let alive = true;
    void fetchBranchRule(repo).then((r) => {
      if (alive && r) setChips(chipPrefixes(r.kinds));
    });
    return () => {
      alive = false;
    };
  }, [repo]);
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
      if (typeof j.branch !== "string" || !j.branch) return;
      // A newer Agent also answers the kind and the slug; the resolver then composes the name,
      // keeping {ref} from the work item the session was launched for (ADR 0103 decision 8).
      const kind = typeof j.kind === "string" ? j.kind : "";
      const slug = typeof j.slug === "string" && j.slug ? j.slug : j.branch;
      const composed = repo && kind ? await fetchBranchName(repo, { session: name, kind, slug }) : null;
      if (composed && !composed.name_empty && composed.name) {
        setProposal(composed.name);
        setProposalWhole(true);
      } else {
        setProposal(j.branch);
        setProposalWhole(false);
      }
    } catch {
      toast(tr("sx.ai_suggest_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  // Toggle a prefix: prepend it (swapping any existing known/temp prefix), or strip
  // it when the name already has exactly that prefix. Only the prefix changes, and nothing is
  // asked of the Agent.
  const togglePrefix = (p: string) =>
    setValue((v) => (v.startsWith(p) ? stripKnownPrefix(v, chips) : p + stripKnownPrefix(v, chips)));

  // Adopt the AI proposal. A composed name is taken whole; a bare slug keeps whichever chip
  // prefix the current value carries, so adopting doesn't drop the selected type.
  const adoptProposal = () => {
    const cur = chips.find((p) => value.startsWith(p));
    setValue(proposalWhole || !cur ? proposal : cur + proposal);
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
          {chips.map((p) => (
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
        {/* Advisory only (decision 8): Save stays enabled. */}
        {warnings.length > 0 && (
          <ul className="launch-branch-warns" role="status">
            {warnings.map((w) => (
              <li key={w.code + w.message} title={w.message}>
                <Icon name="warning" /> {warningText(w)}
              </li>
            ))}
          </ul>
        )}
        {/* Settings > AI assistance > "branch name suggestions": its own switch, and the
            button is hidden when it is off. */}
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
