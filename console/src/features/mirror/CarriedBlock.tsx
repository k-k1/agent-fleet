// CarriedBlock — the interaction that was on screen when the session stopped
// (docs/log/75 §75.6).
//
// The decisive difference from PendingQuestions (the pending card) is how it is answered.
// A pending card fires a key sequence (Down/Enter) at a live TUI modal; a carried
// interaction has no modal — it died with the session and `claude --resume` does not bring
// it back (an unanswered tool_use is bypassed through the parent pointer and drops out of
// the conversation tree; measured, docs/log/75 §75.10 A). The answer can therefore only be
// delivered as prose, and this component builds no key sequence at all.
//
// The card is kept separate so that invariant is enforced structurally: the key-firing code
// (questionKeys) is not imported here, so no carried-interaction button can reach a live
// pane with Down/Enter.
import { useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import { sessionCarriedAnswer } from "../../core/api/client.ts";
import type { CarriedInteraction, CarriedAnswerInput } from "../../core/api/client.ts";
import { PendingQuestions } from "./PendingQuestions.tsx";
import { PlanBlock } from "./transcript/blocks.tsx";

export function CarriedBlock({
  carried,
  session,
  agentName,
  onDone,
  onError,
  onOpenPlan,
}: {
  carried: CarriedInteraction;
  session: string;
  agentName: string;
  /** Send/discard was accepted — retract the card and let polling take over. */
  onDone: () => void;
  onError: (message: string) => void;
  onOpenPlan?: (plan: string) => void;
}) {
  const [sending, setSending] = useState(false);
  const [feedback, setFeedback] = useState("");

  const send = async (body: Parameters<typeof sessionCarriedAnswer>[1]) => {
    if (sending) return;
    setSending(true);
    const r = await sessionCarriedAnswer(session, body);
    setSending(false);
    // Silence is indistinguishable from success (docs/build/92 §7), so always toast a failure.
    if (!r.ok) {
      onError(r.message || tr("err.send_failed"));
      return;
    }
    onDone();
  };

  const title =
    carried.kind === "question"
      ? tr("mirror.carried_question")
      : carried.kind === "plan"
        ? tr("mirror.carried_plan")
        : tr("mirror.carried_permission");

  return (
    <div className="mirror-turn assistant mt-carried" data-testid="carried-card">
      <div className="mirror-turn-head">
        <span className="mt-who">{agentName}</span>
        <span className="mt-model muted">{title}</span>
      </div>
      <div className="mirror-turn-body">
        {/* Say up front why this card is here: it can look like a question the user already
            answered has come back, so state that the answer never arrived and that
            answering now resumes the session. */}
        <div className="mt-carried-note muted">
          <Icon name="info" /> {tr("mirror.carried_note")}
        </div>
        {carried.text && <div className="mt-carried-text">{carried.text}</div>}
        {carried.kind === "question" && (
          <PendingQuestions
            questions={carried.questions || []}
            sending={sending}
            // Block the key-driven entry points: a carried interaction has no modal to aim at.
            onSubmitKeys={() => onError(tr("mirror.carried_no_keys"))}
            onSubmitSeq={() => onError(tr("mirror.carried_no_keys"))}
            onSubmitAnswers={(answers: CarriedAnswerInput[]) => void send({ decision: "answer", answers })}
            onCancel={() => void send({ decision: "discard" })}
            cancelLabel={tr("mirror.carried_discard")}
            submitLabel={tr("mirror.carried_send")}
          />
        )}
        {carried.kind === "plan" && (
          <>
            <PlanBlock plan={carried.plan} session={session} onOpen={carried.plan && onOpenPlan ? () => onOpenPlan(carried.plan!) : undefined} />
            <textarea
              className="mq-freetext"
              rows={2}
              placeholder={tr("mirror.carried_plan_feedback_ph")}
              value={feedback}
              disabled={sending}
              onChange={(e) => setFeedback(e.target.value)}
            />
            <div className="mq-submit-row mq-footer">
              <button type="button" className="ghost" disabled={sending} onClick={() => void send({ decision: "discard" })}>
                <Icon name="close" /> {tr("mirror.carried_discard")}
              </button>
              <button
                type="button"
                className="ghost"
                disabled={sending}
                onClick={() => void send({ decision: "reject", feedback })}
              >
                {tr("mirror.carried_plan_reject")}
              </button>
              {/* Approving here is an instruction to execute, not just an approval: measured
                  (docs/log/75 §75.10 E), a prose approval makes claude run the plan rather than
                  re-issue ExitPlanMode. The decision cannot be undone, so confirm first. */}
              <button
                type="button"
                className="btn primary"
                disabled={sending}
                onClick={() => {
                  if (!window.confirm(tr("mirror.carried_plan_approve_confirm"))) return;
                  void send({ decision: "approve", feedback });
                }}
              >
                {tr("mirror.carried_plan_approve")}
              </button>
            </div>
          </>
        )}
        {carried.kind === "permission" && (
          <>
            {/* A permission answer cannot reach a dead tool call; only the fact of it can be
                carried. So the card shows what was asked and offers the single "continue"
                action. */}
            <div className="mt-perm-msg">{carried.permission}</div>
            <div className="mq-submit-row mq-footer">
              <button type="button" className="ghost" disabled={sending} onClick={() => void send({ decision: "discard" })}>
                <Icon name="close" /> {tr("mirror.carried_discard")}
              </button>
              <button type="button" className="btn primary" disabled={sending} onClick={() => void send({ decision: "continue" })}>
                {tr("mirror.carried_continue")}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
