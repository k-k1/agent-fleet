import type { ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { PlanBlock } from "../transcript/blocks.tsx";
import { PendingQuestions } from "../PendingQuestions.tsx";
import { questionDraftKey } from "../questionDraft.ts";
import type { InteractionAnswer } from "../../../core/api/client.ts";
import type { PendingApproval, Question } from "../transcript/types.ts";

// Pending cards (plan approval, permission request, question) stack at the end of the transcript
// in the same shape as a turn. They are not in the jsonl, so they cannot be grouped and are
// rendered outside TranscriptView; but making them look different too would read as "this is not
// part of the conversation", so they share the .mirror-turn shell.
function PendingTurn({ agentName, note, children }: { agentName: string; note: string; children: ReactNode }) {
  return (
    <div className="mirror-turn assistant">
      <div className="mirror-turn-head">
        <span className="mt-who">{agentName}</span>
        <span className="mt-model muted">{note}</span>
      </div>
      <div className="mirror-turn-body">{children}</div>
    </div>
  );
}

/** Waiting for ExitPlanMode approval. Why approve/reject are driven the way they are is
 *  documented at the onApprove / onReject call site. */
export function PlanPendingCard({
  agentName,
  plan,
  session,
  sending,
  sendDisabled,
  onOpen,
  onSendComments,
  onApprove,
  onReject,
  onReview,
}: {
  agentName: string;
  plan: string;
  session: string;
  sending: boolean;
  sendDisabled: string;
  onOpen: () => void;
  onSendComments: () => void;
  onApprove: () => void;
  onReject: () => void;
  onReview: () => void;
}) {
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.plan_pending")}>
      <PlanBlock
        plan={plan}
        session={session}
        pending
        sending={sending}
        onOpen={onOpen}
        onSendComments={onSendComments}
        sendDisabled={sendDisabled}
        onApprove={onApprove}
        onReject={onReject}
        onReview={onReview}
      />
    </PendingTurn>
  );
}

/** Tool permission prompt. All three choices drive the TUI modal by key, so their order is
 *  itself meaningful. */
export function PermissionCard({
  agentName,
  message,
  sending,
  onAllow,
  onAlwaysAllow,
  onDeny,
}: {
  agentName: string;
  message: string;
  sending: boolean;
  onAllow: () => void;
  onAlwaysAllow: () => void;
  onDeny: () => void;
}) {
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.perm_pending")}>
      <div className="mt-perm">
        <div className="mt-perm-head">
          <Icon name="shield" /> {tr("mirror.perm_asking")}
        </div>
        <div className="mt-perm-msg">{message}</div>
        <div className="mt-perm-actions">
          <button type="button" className="btn primary mt-perm-btn" disabled={sending} onClick={onAllow}>
            <Icon name="check" /> {tr("mirror.allow")}
          </button>
          <button
            type="button"
            className="ghost mt-perm-btn"
            disabled={sending}
            title={tr("mirror.auto_allow")}
            onClick={onAlwaysAllow}
          >
            {tr("mirror.always_allow")}
          </button>
          <button type="button" className="ghost mt-perm-btn" disabled={sending} onClick={onDeny}>
            <Icon name="close" /> {tr("mirror.deny")}
          </button>
        </div>
        <div className="mt-perm-hint muted">{tr("mirror.perm_hint")}</div>
      </div>
    </PendingTurn>
  );
}

/** A managed session's tool approval.
 *
 *  It looks like PermissionCard on purpose and answers nothing like it. PermissionCard's three
 *  buttons drive a TUI modal by keystroke, which a managed session has no pane for; this one
 *  answers the pending Interaction by id through /respond, so allow and deny are the only two
 *  choices the runtime actually offers (ADR 0095 decision 13: `onRequest` mode presents exactly
 *  allow_once and abort, so there is no scope selector to render).
 *
 *  The subject is shown in full rather than summarised: a member approving `a | b` is approving
 *  both, and the parsed argv of each stage is the only place the second one is visible. */
export function ApprovalCard({
  agentName,
  approval,
  sending,
  onAllow,
  onDeny,
}: {
  agentName: string;
  approval: PendingApproval;
  sending: boolean;
  onAllow: () => void;
  onDeny: () => void;
}) {
  const req = approval.request;
  const stages = req.stages ?? [];
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.perm_pending")}>
      <div className="mt-perm">
        <div className="mt-perm-head">
          <Icon name="shield" /> {tr("mirror.approval_asking")}
          {req.tool ? <span className="mt-perm-tool muted"> {req.tool}</span> : null}
        </div>
        <div className="mt-perm-msg mt-approval-subject">{req.command || req.summary}</div>
        {stages.length > 1 && (
          // Only worth the room when there is more than one stage: for a single command the
          // argv repeats the line above it.
          <ol className="mt-approval-stages">
            {stages.map((argv, i) => (
              <li key={i}>
                <code>{argv.join(" ")}</code>
              </li>
            ))}
          </ol>
        )}
        {(req.protectedWrite || req.judgeEscalated) && (
          <div className="mt-approval-flags">
            {req.protectedWrite && <span className="mt-approval-flag warn">{tr("mirror.approval_protected")}</span>}
            {req.judgeEscalated && <span className="mt-approval-flag">{tr("mirror.approval_escalated")}</span>}
          </div>
        )}
        <div className="mt-perm-actions">
          <button type="button" className="btn primary mt-perm-btn" disabled={sending} onClick={onAllow}>
            <Icon name="check" /> {tr("mirror.allow")}
          </button>
          <button type="button" className="ghost mt-perm-btn" disabled={sending} onClick={onDeny}>
            <Icon name="close" /> {tr("mirror.deny")}
          </button>
        </div>
        <div className="mt-perm-hint muted">{tr("mirror.approval_hint")}</div>
      </div>
    </PendingTurn>
  );
}

/** A pending AskUserQuestion. The prompt body that scrolled past just before (pendingText) is
 *  shown alongside it. */
export function QuestionCard({
  agentName,
  session,
  questions,
  pendingText,
  repo,
  sending,
  answerMode,
  multiPage,
  writeIn,
  onOpenFile,
  onSubmitKeys,
  onSubmitSeq,
  onRespond,
  onCancel,
}: {
  agentName: string;
  session: string;
  questions: Question[];
  pendingText: string;
  repo: string | null;
  sending: boolean;
  answerMode: "claude" | "menu";
  multiPage: boolean;
  writeIn: boolean;
  onOpenFile: (path: string, line?: number, column?: number) => void;
  onSubmitKeys: (keys: string[]) => void | Promise<boolean | void>;
  onSubmitSeq: (seq: Array<{ k?: string; t?: string }>) => void | Promise<boolean | void>;
  onRespond?: (answers: InteractionAnswer[]) => void | Promise<boolean | void>;
  onCancel: () => void;
}) {
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.questioning")}>
      {pendingText && <MarkdownView source={pendingText} repo={repo} onOpenFile={onOpenFile} />}
      <PendingQuestions
        key={"pq-" + (questions[0]?.question || "")}
        questions={questions}
        draftKey={questionDraftKey(session)}
        sending={sending}
        onSubmitKeys={onSubmitKeys}
        onSubmitSeq={onSubmitSeq}
        onRespond={onRespond}
        // Cancel maps to the same stop primitive as the chat stop button: TUI sends
        // Escape (dismisses the AUQ modal, doesn't mark a turn), managed calls
        // Interrupt. Either way the pending question clears and the composer is free.
        onCancel={onCancel}
        answerMode={answerMode}
        multiPage={multiPage}
        writeIn={writeIn}
      />
    </PendingTurn>
  );
}

/** Typing indicator. The stop button lives here so it never shifts the composer; see the note
 *  at the button. */
export function TypingRow({
  agentName,
  sending,
  onStop,
}: {
  agentName: string;
  sending: boolean;
  onStop: () => void;
}) {
  return (
    <div className="mirror-typing" aria-label={tr("mirror.typing", { name: agentName })}>
      <span className="mt-who">{agentName}</span>
      <span className="typing-dots">
        <i />
        <i />
        <i />
      </span>
      {/* Stop the running turn (Escape) — lives with the typing indicator so it shows
          while working OR while a background run (subagent / workflow) lingers on an
          otherwise-idle session, and never shifts the composer. */}
      <button type="button" className="ghost mirror-stop" disabled={sending} title={tr("mirror.stop_run")} onClick={onStop}>
        <Icon name="debug-stop" /> {tr("chat.stop")}
      </button>
    </div>
  );
}
