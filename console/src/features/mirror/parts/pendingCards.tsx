import { useRef, type ReactNode } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { usePrefersReducedMotion } from "../../../lib/device.ts";
import { MarkdownView } from "../../viewer/MarkdownView.tsx";
import { useTypewriter } from "../useTypewriter.ts";
import { PlanBlock } from "../transcript/blocks.tsx";
import { PendingQuestions } from "../PendingQuestions.tsx";
import { questionDraftKey } from "../questionDraft.ts";
import { useQuestionTranslate } from "../questionTranslate.ts";
import { StopControl } from "./StopControl.tsx";
import type { TranscriptTranslateWiring } from "../useTranslate.ts";
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
  translate,
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
  translate?: TranscriptTranslateWiring;
}) {
  const tx = useQuestionTranslate(translate, questions, pendingText);
  const lead = tx?.lead ?? pendingText;
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.questioning")}>
      {lead && <MarkdownView source={lead} repo={repo} onOpenFile={onOpenFile} />}
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
        translate={tx}
      />
    </PendingTurn>
  );
}

/** Typing indicator. The stop button lives here so it never shifts the composer; see the note
 *  at the button. */
/** The reply the agent is still writing (#1250): what it has streamed so far and the transcript
 *  does not hold yet. The Agent stops sending it the moment the real turn lands, which then takes
 *  its place, so it carries no actions of its own.
 *
 *  `mode` is the "stream replies" setting: "lines" shows each poll's text as it comes, "typewriter"
 *  types the new text out between polls (#1274, useTypewriter) — through MarkdownView's streaming
 *  render, which is the cheap one (no link wiring, no copy buttons, no mermaid) and ends in a
 *  caret, since it runs once per frame. Reduced motion turns typewriter into lines. */
export function LiveReplyCard({
  agentName,
  text,
  repo,
  onOpenFile,
  mode = "lines",
}: {
  agentName: string;
  text: string;
  repo: string | null;
  onOpenFile: (path: string, line?: number, column?: number) => void;
  mode?: "lines" | "typewriter";
}) {
  const reducedMotion = usePrefersReducedMotion();
  const typewriter = mode === "typewriter" && !reducedMotion;
  const host = useRef<HTMLDivElement>(null);
  const shown = useTypewriter(text, typewriter, host);
  return (
    <PendingTurn agentName={agentName} note={tr("mirror.writing")}>
      <div ref={host} className={typewriter ? "mirror-live typewriter" : "mirror-live"}>
        <MarkdownView source={shown} repo={repo} onOpenFile={onOpenFile} streaming={typewriter} />
      </div>
    </PendingTurn>
  );
}

export function TypingRow({
  agentName,
  typing = true,
  managed,
  queuedCount,
  onStop,
  onDiscard,
}: {
  agentName: string;
  /** false while a question or approval card waits on the member: the agent is not typing,
   *  but a Managed session keeps its brake reachable (ADR 0105 decision 3). */
  typing?: boolean;
  managed: boolean;
  queuedCount: number;
  onStop: () => void;
  onDiscard: () => void;
}) {
  return (
    <div className="mirror-typing" aria-label={typing ? tr("mirror.typing", { name: agentName }) : undefined}>
      {typing && (
        <>
          <span className="mt-who">{agentName}</span>
          <span className="typing-dots">
            <i />
            <i />
            <i />
          </span>
        </>
      )}
      {/* Stop the running turn (Escape) — lives with the typing indicator so it shows
          while working OR while a background run (subagent / workflow) lingers on an
          otherwise-idle session, and never shifts the composer. */}
      <StopControl managed={managed} queuedCount={queuedCount} onStop={onStop} onDiscard={onDiscard} />
    </div>
  );
}
