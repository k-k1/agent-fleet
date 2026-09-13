// PromptHelpModal — layer B of ADR 0081 decision 7: one press, one `askAssistant()` call,
// one proposal the person accepts or throws away.
//
// The shape is MemoTidyModal's and the TTS summary's, deliberately: an unpersisted one-shot
// turn on the member's own CLI login, previewed before it is applied. "Nothing is applied on
// its own" is not a comment here, it is the structure — this component never calls `patch`,
// it hands the accepted fields back through `onUse` and the caller decides.
import { useState } from "react";
import { askAssistant } from "../../chat/api.ts";
import { errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Modal } from "../../../ui/Modal.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { buildPromptHelpMessage, parseProposal, type PromptProposal } from "../prompthelp.ts";
import type { ImagegenLora, ImagegenModel } from "../wire.ts";

interface Props {
  model: ImagegenModel | null;
  loras: ImagegenLora[];
  alwaysNegative: string;
  negativeReaches: boolean;
  onUse: (prompt: string, negative: string | null) => void;
  onClose: () => void;
}

export function PromptHelpModal({ model, loras, alwaysNegative, negativeReaches, onUse, onClose }: Props) {
  const tr = useT();
  const [intent, setIntent] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [proposal, setProposal] = useState<PromptProposal | null>(null);

  const ask = async () => {
    if (!intent.trim() || busy) return;
    setBusy(true);
    setErr("");
    setProposal(null);
    try {
      const r = await askAssistant(
        buildPromptHelpMessage({
          intent,
          model,
          loras,
          rowNegative: model?.negative,
          alwaysNegative,
          negativeReaches,
        }),
      );
      if (r.error) {
        setErr(errText(r.error) || tr("imggen.help_failed"));
        return;
      }
      const p = parseProposal(r.reply || "");
      if (!p) {
        setErr(tr("imggen.help_parse_failed"));
        return;
      }
      setProposal(p);
    } catch {
      setErr(tr("imggen.help_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={tr("imggen.help_title")} onClose={onClose} className="igen-help" lockClose={busy}>
      {/* ui/Modal carries no padding of its own — head / body / foot each own their spacing,
          and content placed directly under it sticks to the frame (src/test/modalBody). */}
      <div className="ui-modal-body">
        <p className="igen-hint">{tr("imggen.help_lead")}</p>
        <label className="igen-field">
          <span className="igen-label">{tr("imggen.help_intent")}</span>
          <textarea
            className="igen-prompt"
            rows={3}
            value={intent}
            autoFocus
            placeholder={tr("imggen.help_intent_ph")}
            onChange={(e) => setIntent(e.target.value)}
          />
        </label>
        <div className="igen-actions">
          <button
            type="button"
            className="ui-btn ui-btn-primary"
            disabled={busy || !intent.trim()}
            onClick={() => void ask()}
          >
            {busy ? <Icon name="loading" spin /> : <Icon name="sparkle" />}{" "}
            {busy ? tr("imggen.help_asking") : tr("imggen.help_ask")}
          </button>
        </div>
        {err && <div className="igen-err">{err}</div>}
        {proposal && (
          <div className="igen-proposal">
            <h4>{tr("imggen.help_proposal")}</h4>
            <pre className="igen-proposal-text">{proposal.prompt}</pre>
            {proposal.negative && (
              <>
                <h4>{tr("imggen.negative")}</h4>
                <pre className="igen-proposal-text">{proposal.negative}</pre>
              </>
            )}
            {proposal.note && (
              <p className="igen-hint">
                {tr("imggen.help_note")}: {proposal.note}
              </p>
            )}
            <div className="igen-actions">
              <button
                type="button"
                className="ui-btn ui-btn-primary"
                onClick={() => onUse(proposal.prompt, proposal.negative || "")}
              >
                {tr("imggen.help_use")}
              </button>
              <button type="button" className="ui-btn" onClick={() => onUse(proposal.prompt, null)}>
                {tr("imggen.help_use_prompt")}
              </button>
              <button type="button" className="ui-btn ui-btn-ghost" onClick={onClose}>
                {tr("imggen.help_discard")}
              </button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
