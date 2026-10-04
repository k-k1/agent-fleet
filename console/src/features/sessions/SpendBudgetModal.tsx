// SpendBudgetModal — set a session's spend budget (#1054), and the one action a budget stop
// asks for: raise the cap and resume. Opened from the session menu, the budget chip in the
// mirror and the "spend-budget" notification.
//
// The resume is the ordinary start, after the cap is written: the Agent clears the crossing
// (and the stop it armed) only when the new cap is above the spend, so a resume with a cap still
// at or under it would run one more turn and stop again. The button therefore stays disabled
// until the value clears the spend — or is 0, which removes the budget.
import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Modal } from "../../ui/Modal.tsx";
import { Button } from "../../ui/Button.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { errText, sessionSetSpendCap, sessionSpend } from "../../core/api/client.ts";
import type { SessionSpend } from "../../core/api/client.ts";
import { useSessionsStore } from "./store.ts";
import { fmtSpend, parseCap, suggestedRaise } from "./spendBudget.ts";
import type { Session } from "../../types/session.ts";

interface SpendBudgetModalProps {
  s: Pick<Session, "name" | "alive" | "spendCapUsd" | "spendCapHitAt">;
  onClose: () => void;
}

export function SpendBudgetModal({ s, onClose }: SpendBudgetModalProps) {
  const tr = useT();
  const toast = useToast();
  const cap = s.spendCapUsd ?? 0;
  // A stopped session that crossed its budget is the "raise and resume" case.
  const resume = !!s.spendCapHitAt && !s.alive;
  const [spend, setSpend] = useState<SessionSpend | null>(null);
  const [value, setValue] = useState(cap > 0 ? String(cap) : "");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let live = true;
    void sessionSpend(s.name)
      .then((r) => {
        if (!live || r?.error) return;
        setSpend(r);
        if (resume) setValue(String(suggestedRaise(r.spendCapUsd ?? cap, r.spendUsd)));
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [s.name, resume, cap]);

  const next = parseCap(value);
  const spent = spend?.spendUsd ?? 0;
  const invalid = Number.isNaN(next);
  const stillUnder = !invalid && next > 0 && spend !== null && next <= spent;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (saving || invalid || (resume && stillUnder)) return;
    setSaving(true);
    try {
      const r = await sessionSetSpendCap(s.name, next);
      if (r?.error) {
        toast(errText(r.error));
        return;
      }
      if (resume) {
        const ok = await useSessionsStore.getState().start(s.name);
        if (!ok) {
          toast(tr("sess.budget_resume_failed"));
          return;
        }
      }
      toast(next > 0 ? tr("sess.budget_saved", { cap: "$" + next.toFixed(2) }) : tr("sess.budget_removed"), {
        kind: "success",
      });
      void useSessionsStore.getState().refresh();
      onClose();
    } catch {
      toast(tr("sx.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={resume ? tr("sess.budget_raise_title") : tr("sess.budget_title")}
      onClose={onClose}
      as="form"
      onSubmit={submit}
      lockClose={saving}
    >
      <div className="ui-modal-body">
        {resume && <p className="spend-budget-lead">{tr("sess.budget_raise_lead", { cap: "$" + cap.toFixed(2) })}</p>}
        <p className="spend-budget-spent" data-testid="spend-budget-spent">
          {spend === null
            ? tr("sess.budget_loading")
            : spend.priced
              ? tr("sess.budget_spent", { spend: fmtSpend(spent) })
              : tr("sess.budget_unpriced")}
        </p>
        <label className="ui-field">
          <span className="ui-field-label">{tr("sess.budget_label")}</span>
          <input
            type="text"
            inputMode="decimal"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onFocus={(e) => e.target.select()}
            placeholder={tr("sess.budget_ph")}
            aria-invalid={invalid || undefined}
            autoFocus
          />
          <span className="ui-field-hint">
            {invalid
              ? tr("sess.budget_invalid")
              : stillUnder
                ? tr("sess.budget_still_under")
                : tr("sess.budget_hint", { factor: String(spend?.hardFactor ?? 2) })}
          </span>
        </label>
      </div>
      <footer className="ui-modal-foot">
        <Button variant="ghost" onClick={onClose} disabled={saving}>
          {tr("sx.cancel")}
        </Button>
        <Button variant="primary" type="submit" disabled={saving || invalid || (resume && stillUnder)}>
          {resume ? tr("sess.budget_raise_resume") : tr("sx.save")}
        </Button>
      </footer>
    </Modal>
  );
}
