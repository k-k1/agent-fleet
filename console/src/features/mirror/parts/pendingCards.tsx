import type { ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { PlanBlock } from "../transcript/blocks.tsx";
import { PendingQuestions } from "../PendingQuestions.tsx";
import type { InteractionAnswer } from "../../../core/api/client.ts";
import type { Question } from "../transcript/types.ts";

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

/** A pending AskUserQuestion. The prompt body that scrolled past just before (pendingText) is
 *  shown alongside it. */
export function QuestionCard({
  agentName,
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
  questions: Question[];
  pendingText: string;
  repo: string | null;
  sending: boolean;
  answerMode: "claude" | "menu";
  multiPage: boolean;
  writeIn: boolean;
  onOpenFile: (path: string, line?: number, column?: number) => void;
  onSubmitKeys: (keys: string[]) => void;
  onSubmitSeq: (seq: Array<{ k?: string; t?: string }>) => void;
  onRespond?: (answers: InteractionAnswer[]) => void;
  onCancel: () => void;
}) {
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.questioning")}>
      {pendingText && <MarkdownView source={pendingText} repo={repo} onOpenFile={onOpenFile} />}
      <PendingQuestions
        key={"pq-" + (questions[0]?.question || "")}
        questions={questions}
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
