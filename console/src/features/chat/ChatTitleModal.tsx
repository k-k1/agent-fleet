// ChatTitleModal — manual title edit for an assistant chat (rename icon in the left
// rail). Mirrors SessionTitleModal.tsx: the "ask the AI to suggest one" button fetches a
// candidate via the preview-only /title/suggest endpoint and shows it as a proposal the user
// applies explicitly — it never clobbers what they typed.
import { useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { SESSION_TITLE_MAX } from "../../lib/sessionTitle.ts";
import { useSettings } from "../../lib/settings.ts";
import { errText, type ApiError } from "../../core/api/client.ts";
import { chatRename, chatSuggestTitle } from "./api.ts";

interface ChatTitleModalProps {
  id: string;
  title: string;
  onClose: () => void;
  onSaved: () => void;
}

export function ChatTitleModal({ id, title, onClose, onSaved }: ChatTitleModalProps) {
  const [value, setValue] = useState(title);
  const [saving, setSaving] = useState(false);
  const [suggesting, setSuggesting] = useState(false);
  const [proposal, setProposal] = useState("");
  const toast = useToast();
  const tr = useT();
  const aiSuggest = useSettings().assistantTitleSuggest;
  const busy = saving || suggesting;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const t = value.trim();
    if (!t) return;
    setSaving(true);
    try {
      // apiJSON resolves a server error as {error} rather than throwing, so on failure the
      // modal stays open.
      const j = await chatRename(id, t);
      const err = (j as { error?: ApiError }).error;
      if (err) {
        toast(errText(err));
        return;
      }
      onSaved();
      onClose();
    } catch {
      toast(tr("common.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const suggest = async () => {
    if (busy) return;
    setSuggesting(true);
    try {
      const j = await chatSuggestTitle(id);
      if (j.error) {
        toast(errText(j.error));
        return;
      }
      if (typeof j.suggestedTitle === "string" && j.suggestedTitle) setProposal(j.suggestedTitle);
    } catch {
      toast(tr("asst.suggest_fetch_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  return (
    <Modal title={tr("asst.title_rename_title")} onClose={onClose} as="form" onSubmit={submit} lockClose={saving}>
      <div className="ui-modal-body">
        <label className="ui-field">
          <span className="ui-field-label">{tr("asst.title_label")}</span>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={tr("asst.title_ph")}
            maxLength={SESSION_TITLE_MAX}
            autoFocus
          />
        </label>
        {/* Settings > AI assistance, "suggest chat titles". Hide the button when it is off:
            always showing it only bought a 400 (feature_disabled) toast on click. */}
        {aiSuggest && (
          <div>
            <Button icon={suggesting ? "loading" : "lightbulb"} onClick={suggest} disabled={busy}>
              {tr("asst.ai_suggest")}
            </Button>
          </div>
        )}
        {proposal && (
          <div className="sm-proposal">
            <span className="sm-proposal-label">{tr("asst.proposal")}</span>
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
              {tr("asst.adopt")}
            </Button>
          </div>
        )}
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          {tr("common.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={saving}>
          {tr("common.save")}
        </Button>
      </footer>
    </Modal>
  );
}
