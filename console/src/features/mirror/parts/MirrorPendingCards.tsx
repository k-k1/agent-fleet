import type { Session } from "../../../types/session.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { CarriedBlock } from "../CarriedBlock.tsx";
import { PLAN_APPROVE_KEYS } from "../planDecision.ts";
import { stopRowVisible } from "../stopQueue.ts";
import type { useTranslate } from "../useTranslate.ts";
import { ApprovalCard, LiveReplyCard, PlanPendingCard, PermissionCard, QuestionCard, TypingRow } from "./pendingCards.tsx";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { usePlanActions } from "./usePlanActions.ts";

/**
 * MirrorPendingCards draws what waits on the member below the transcript, inside the scroll
 * box: the carried interaction, the pending plan / approval / permission / question card, the
 * reply being written and the typing row with its stop button.
 */
export function MirrorPendingCards({
  session,
  sessionMeta,
  managed,
  agentName,
  toast,
  translate,
  st,
  actions,
  plan,
}: {
  session: string;
  sessionMeta?: Session | null;
  managed: boolean;
  agentName: string;
  toast: ReturnType<typeof useToast>;
  translate: ReturnType<typeof useTranslate>;
  st: MirrorState;
  actions: MirrorActions;
  plan: ReturnType<typeof usePlanActions>;
}) {
  const {
    busy, queuedCount, pending, pendingText, liveMode, liveText, pendingPlan, pendingPerm, pendingApproval,
    carried, setCarried, markRejected, sending,
  } = st;
  const { sendKeys, sendSeq, sendInterrupt, cancelQuestion, sendApproval, sendRespond } = actions;
  const { openPlan, openFile, planSendBlocked, sendPlanComments, reviewPlanElsewhere } = plan;
  return (
    <>
      {carried && (
        // Carried interaction (docs/log/75). Unlike the pending card this sends no keys at
        // all: there is no modal left to aim at, and the Agent delivers the answer as prose
        // after resuming.
        <CarriedBlock
          carried={carried}
          session={session}
          agentName={agentName}
          onOpenPlan={openPlan}
          onError={(m) => toast(m)}
          onDone={() => setCarried(null)}
          translate={translate}
        />
      )}
      {pendingPlan && (
        <PlanPendingCard
          agentName={agentName}
          plan={pendingPlan}
          session={session}
          sending={sending}
          sendDisabled={planSendBlocked}
          onOpen={() => openPlan(pendingPlan)}
          onSendComments={() => void sendPlanComments(pendingPlan)}
          onApprove={() => {
            // A rejected plan may be refined and re-presented with identical Markdown.
            // The optimistic marker is keyed by that Markdown (the pending payload has
            // no tool-use id), so it belongs only until the next decision. Clear it
            // before approving the new presentation; its real tool_result still keeps
            // the older historical card correctly badged as rejected.
            markRejected(pendingPlan, false);
            void sendKeys([...PLAN_APPROVE_KEYS]);
          }}
          // Reject = interrupt (Escape), which falls back to keep-planning. The number and
          // order of the ExitPlanMode menu's options depend on the claude version, so a
          // position-fixed key sequence (aiming at "4. Tell Claude what to change" with
          // Down×3) wraps around to the leading "Yes" row on a shorter menu and approves the
          // plan the user meant to reject — a real incident. An interrupt closes the modal
          // independently of layout, returns to plan mode and releases the composer; the
          // tool_result becomes an interrupt, which planDecision.isRejected picks up. See
          // planDecision.ts.
          onReject={() => {
            markRejected(pendingPlan, true); // optimistic "rejected" badge; planOutcome reconciles it
            void sendInterrupt();
          }}
          onReview={() => void reviewPlanElsewhere(pendingPlan)}
        />
      )}
      {pendingApproval && !pending && !pendingPlan && (
        <ApprovalCard
          agentName={agentName}
          approval={pendingApproval}
          sending={sending}
          onAllow={() => void sendApproval(pendingApproval.id, true)}
          onDeny={() => void sendApproval(pendingApproval.id, false)}
        />
      )}
      {pendingPerm && !pendingApproval && !pending && !pendingPlan && (
        // Defense-in-depth: a question/plan always wins over a generic permission
        // dialog (the server already suppresses the permission in that case). This
        // guards against a poll race ever showing allow/deny over an AskUserQuestion,
        // whose buttons would send keystrokes that mis-answer the question underneath.
        <PermissionCard
          agentName={agentName}
          message={pendingPerm}
          sending={sending}
          onAllow={() => sendKeys(["Enter"])}
          onAlwaysAllow={() => sendKeys(["Down", "Enter"])}
          onDeny={() => sendKeys(["Down", "Down", "Enter"])}
        />
      )}
      {pending && pending.length > 0 && (
        <QuestionCard
          agentName={agentName}
          session={session}
          questions={pending}
          pendingText={pendingText}
          repo={sessionMeta?.repo ?? null}
          sending={sending}
          answerMode={sessionMeta?.kind === "claude" ? "claude" : "menu"}
          multiPage={sessionMeta?.kind === "codex"}
          writeIn={sessionMeta?.kind === "agy"}
          onOpenFile={openFile}
          onSubmitKeys={sendKeys}
          onSubmitSeq={sendSeq}
          onRespond={
            // A managed session is pinned to the semantic route whether or not an id is
            // present: falling back to keys/seq would drive a tmux pane that does not
            // exist. A question missing its id (a transitional or resyncing case up to P2)
            // is rejected server-side with bad_interaction, and sendRespond toasts that.
            managed ? (answers) => sendRespond(pending[0]?.id || "", answers) : undefined
          }
          // Managed: decline the question through /respond, which every driver answers with the
          // runtime's own rejection (codex alone turns it into a stop, ADR 0105 decision 7). A
          // stop here would be a second stop inside an episode and discard the queue. Terminal
          // (CLI): the card's Cancel is the Esc, as before.
          onCancel={() => void (managed ? cancelQuestion(pending[0]?.id || "") : sendInterrupt())}
          translate={translate}
        />
      )}
      {liveText && busy && !pending && !pendingPlan && !pendingPerm && !pendingApproval && (
        // Above the typing row: that row keeps the stop button and says the turn is still
        // running; this is what the turn has written so far.
        <LiveReplyCard
          agentName={agentName}
          text={liveText}
          repo={sessionMeta?.repo ?? null}
          onOpenFile={openFile}
          mode={liveMode === "typewriter" ? "typewriter" : "lines"}
        />
      )}
      {stopRowVisible({ managed, busy, queued: queuedCount > 0, question: !!pending, approval: !!pendingApproval }) && (
        <TypingRow
          agentName={agentName}
          typing={busy && !pending}
          managed={managed}
          queuedCount={queuedCount}
          onStop={() => void sendInterrupt()}
          onDiscard={() => void sendInterrupt(true)}
        />
      )}
    </>
  );
}
