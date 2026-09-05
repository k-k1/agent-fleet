// SessionTitleModal — manual title edit (⋯ → "rename"). "Suggest with AI" fetches a
// candidate via the preview-only /title/suggest endpoint and shows it as a
// proposal the user applies explicitly — it never clobbers what they typed.
// The suggest button only renders for kinds with a transcript (claude/codex/
// opencode) — shell/ssm have no conversation log, so suggestion always failed.
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { SESSION_TITLE_MAX } from "../../lib/sessionTitle.ts";
import { apiJSON, errText } from "../../core/api/client.ts";
import { agentOf } from "../../agents/registry.ts";
import { useSettings } from "../../lib/settings.ts";

interface SessionTitleModalProps {
  name: string;
  kind: string;
  title: string;
  onClose: () => void;
  onSaved: () => void;
}

export function SessionTitleModal({ name, kind, title, onClose, onSaved }: SessionTitleModalProps) {
  // A kind without a transcript cannot be suggested for (capability), and with
  // Settings > AI assistance > "session title suggestions" off the button is not rendered
  // at all (intent).
  const titleSuggest = useSettings().autoTitleSuggest;
  const canSuggest = agentOf(kind).caps.transcript && titleSuggest;
  const [value, setValue] = useState(title);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
  const toast = useToast();
  const tr = useT();
  const busy = saving || suggesting;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setSaving(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/title/set`, "POST", {
        title: value.trim(),
      });
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      onSaved();
      onClose();
    } catch {
      toast(tr("sx.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const suggest = async () => {
    if (busy) return;
    setSuggesting(true);
    try {
      const j = await apiJSON(`api/sessions/${encodeURIComponent(name)}/title/suggest`, "POST");
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      if (typeof j.suggestedTitle === "string" && j.suggestedTitle) setProposal(j.suggestedTitle);
    } catch {
      toast(tr("sx.suggest_fetch_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  return (
    <Modal title={tr("sx.title_rename_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">{tr("sx.title_label")}</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={tr("sx.title_ph")}
            maxLength={SESSION_TITLE_MAX}
            autoFocus
          />
          <span className="ui-field-hint">{tr("sx.title_hint")}</span>
        </label>
        {canSuggest && (
          <div>
            {/* Same sparkle icon for an AI suggestion as BranchRenameModal and the mirror */}
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
              onClick={() => {
                setValue(proposal);
                setProposal("");
              }}
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
