import { useEffect, useRef } from "react";
import {
  apiJSON,
  errText,
  sessionTurn,
  sessionInterrupt,
  sessionRemoveQueued,
  sessionDropPendingPeer,
  sessionDismissDiscard,
  sessionCancelInteraction,
  isMemberOrigin,
  sessionRespond,
  sessionApprove,
} from "../../../core/api/client.ts";
import type { Discard, InteractionAnswer, QueueItem, TurnResult } from "../../../core/api/client.ts";
import type { Attachment } from "../../../lib/attachDraft.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { takeLaunchSeed } from "../../../lib/launchSeed.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { withoutDiscarded } from "../pendingEcho.ts";
import { closeStep, restoreStep } from "../stopQueue.ts";
import { nextEchoId, sweptDiscards } from "./sendEcho.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { useMirrorScroll } from "./useMirrorScroll.ts";

const q = encodeURIComponent;

/**
 * useMirrorActions is everything the mirror sends to its session: prompts, modal keystrokes,
 * structured answers and approvals, stops, and the queue / discard / waiting-peer edits. Each
 * self-guards against a stopped Workspace (wsDown) and reports its own failures.
 */
export function useMirrorActions({
  session,
  running,
  managed,
  toast,
  st,
  scroll,
}: {
  session: string;
  running: boolean;
  managed: boolean;
  toast: ReturnType<typeof useToast>;
  st: MirrorState;
  scroll: ReturnType<typeof useMirrorScroll>;
}) {
  const {
    turns, stateSession, applyEchoes, setStatus, setFinalizing, finalizingRef, wasWorkingRef, discards,
    discardNotices, setDiscardNotices, setPendingPeers, queueOpsRef, queuedCount, draft, setDraft, sending,
    setSending, sendingRef, attach, attachments, setHistIdx, statusRef, tickRef, inputRef,
  } = st;

  // Low-level: submit one prompt as a semantic turn op — start when idle, steer when a
  // turn is already running (docs/log/27 §4). The Agent adapts it per driver: tui = the same
  // tmux typing as before (sessionTurn falls back to /input against an old Agent),
  // managed = the turn/start and turn/steer RPCs (P2). The result carries the rejection
  // reason so the caller can drop its optimistic echo AND tell the user why.
  // Only managed sessions pass attachments; the driver turns them into API attachments
  // (docs/log/27 §10.2-3).
  const postInput = (text: string, op: "start" | "steer", attachments?: string[]): Promise<TurnResult> =>
    sessionTurn(session, op, text, attachments);

  // wsDown: the workspace isn't running, so nothing can receive an agent-bound action —
  // it would just 502, and helpers that optimistically flip the UI to "working" would leave
  // that spinner stuck (the poll is frozen while stopped). Every send helper funnels through
  // here: the live composer is already hidden while stopped (MirrorView's !running branch), but
  // the pending permission/question/plan cards and the stop button render OUTSIDE that branch,
  // so each must self-guard. Returns true (and toasts once) when the action must be dropped.
  const wsDown = (): boolean => {
    if (running) return false;
    toast(tr("mirror.ws_stopped"));
    return true;
  };

  // Low-level: send named keys with NO "working" status and NO quick re-poll — used by
  // the plan-mode toggle, which isn't a turn. (The quick re-poll of sendKeys/sendPrompt
  // would fire before the mode actually changed and momentarily revert the optimistic
  // indicator; the regular poll picks up the real mode via paneMode.)
  const postKeys = async (keys: string[]) => {
    if (wsDown()) return; // plan-mode toggle / codex update-menu skip: no agent to key while stopped
    try {
      await apiJSON(`api/sessions/${q(session)}/input`, "POST", { keys });
    } catch {
      /* next poll reconciles */
    }
  };

  // Newest real (jsonl-backed) turn idx currently held, or -1. Used to anchor an
  // optimistic echo so it only reconciles against a turn that arrives after the send.
  const newestIdx = (): number => {
    for (let i = turns.length - 1; i >= 0; i--) {
      if (turns[i].idx !== undefined) return turns[i].idx as number;
    }
    return -1;
  };

  // sendPrompt submits one prompt (the composer). Never used to answer an AUQ —
  // the modal ignores typed text, so a text send would confirm option 1 (docs/build/92).
  // attachments are the API attachments of a managed session (send() chooses between them and
  // weaving paths into the text). The return value says whether the session accepted the send.
  // Most callers can ignore it, but the plan-comment "sent" marker must not: returning void and
  // only toasting the failure folded away comments that never arrived, leaving them impossible
  // to retype (a comment rejected with permission_pending was immediately marked as sent).
  // restoreText is what to write back into the composer on failure. It defaults to the text
  // that was sent, but under tui that text has the attachment-path instructions woven in
  // (buildImagePrompt), so composer sends pass the text the user actually typed — the
  // attachment chips come back too, and restoring the path-bearing text would duplicate the
  // paths on the next attempt.
  const sendPrompt = async (
    text: string,
    attachments?: string[],
    restoreText?: string,
    wire?: string,
  ): Promise<boolean> => {
    const t = (text || "").trim();
    // sendingRef (not the `sending` state alone) guards re-entrancy: two invocations
    // arriving in the same task both read `sending` before either commit lands, but the
    // ref is set synchronously right here, so the second call sees it immediately.
    if ((!t && !attachments?.length) || sendingRef.current) return false;
    // WS down: nothing can receive the prompt (a send would 502). The composer is already
    // hidden while stopped, but other callers (seed prompt, file drop) reach here too — bail
    // before the optimistic echo so a send never looks accepted when it can't be.
    if (wsDown()) return false;
    sendingRef.current = true;
    setSending(true);
    // start = a new turn, steer = a follow-up into the running one. Decided from the real
    // status, before the optimistic flip to "working". Under tui both collapse to the same
    // typing, but managed's turn/start vs turn/steer (P2) depends on the distinction.
    const op = statusRef.current === "working" ? "steer" : "start";
    statusRef.current = "working";
    setStatus("working");
    // Sending is an explicit "take me to the conversation": re-arm auto-follow so the
    // optimistic echo below and the incoming reply are surfaced, even if the user had
    // scrolled up to read history.
    scroll.armFollow();
    // Show the message immediately (optimistic echo) so it never looks lost while claude
    // is busy — reconciled away once its real user turn appears in the transcript.
    const echoId = nextEchoId();
    applyEchoes((p) => [...p, { id: echoId, text: t, sinceIdx: newestIdx(), attachmentPaths: attachments, at: Date.now() }]);
    // The echo keeps the member's words; only the wire carries the studio signal, which the
    // transcript strips again before the echo is reconciled against it (composerSend).
    const res = await postInput(wire || t, op, attachments);
    if (!res.ok) {
      // The send was not accepted: keeping the echo would make it look sent, so drop it,
      // toast the reason and restore the draft that send() already cleared — without
      // clobbering anything the user has started retyping.
      applyEchoes((p) => p.filter((e) => e.id !== echoId));
      toast(res.message || tr("mirror.send_failed"));
      setDraft((d) => d || restoreText || t);
    }
    sendingRef.current = false;
    setSending(false);
    // Pick up the just-logged user turn quickly rather than waiting a full interval.
    setTimeout(() => tickRef.current?.(), 250);
    return res.ok;
  };

  // Launch seed: a session started from "start work" carries a first prompt. The mirror no
  // longer SENDS it — the Agent does, from the create call's initial_prompt (or /input
  // {when_ready} when attachments made the text final only after create; useStartWork.ts).
  // That matters because this view is mounted only while its tab is the selected one:
  // typing it from here meant a session launched into a background tab sat idle until the
  // user came back to it, and then looked as if opening the tab is what sent the message.
  //
  // What is left here is display: show the sent text as an optimistic echo so the chat
  // isn't empty for the seconds between launch and the first turn reaching the transcript.
  // It is dropped by the normal reconciliation once that turn lands.
  //
  // sinceIdx is -1 on purpose. An echo's anchor exists to keep it from matching a turn
  // that predates the send, but this one can only ever match the first turn of a brand-new
  // session — while an anchor taken from newestIdx() would strand it forever whenever the
  // turn is ALREADY in the transcript (delivery won the race, or the pane was opened
  // later). stateSession likewise: on the commit where the `session` prop changes this
  // component still holds the PREVIOUS session's state (useTranscriptPoll's reset is a layout effect,
  // so it lands one render later), and appending an echo there would both anchor it against
  // a foreign transcript and copy the old session's pending echoes into the new one's stash.
  const seededRef = useRef(false);
  useEffect(() => {
    seededRef.current = false; // new session → allow its own seed
  }, [session]);
  useEffect(() => {
    if (seededRef.current || stateSession !== session) return;
    const seed = takeLaunchSeed(session);
    if (!seed) return;
    seededRef.current = true;
    const echoId = nextEchoId();
    applyEchoes((p) => [...p, { id: echoId, text: seed.trim(), sinceIdx: -1, launch: true, at: Date.now() }]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, stateSession]);

  // driveInput posts one modal-driving body ({keys} or {seq}) and — this is the point —
  // does NOT swallow a rejection. api() resolves non-2xx as a value ({error:{code}}), so
  // a `try/await/catch {}` here would never run its catch: a 400 (bad_key, view-nav
  // guard, rate-limit modal) left the card sitting there with no keystroke delivered and
  // no message — pressing the button appeared to do nothing. Answering is the one place where silence is
  // indistinguishable from success, so failures speak — same treatment as sendRespond's
  // managed path. The optimistic 'working' is rolled back too, or the chip claims a turn
  // that never started until the next poll.
  // The boolean says whether the keystrokes actually went out — the question card restores
  // the draft it cleared when they did not (PendingQuestions.fire).
  const driveInput = async (body: { keys?: string[]; seq?: Array<{ k?: string; t?: string }> }): Promise<boolean> => {
    if (sending) return false;
    if (wsDown()) return false; // WS stopped: no agent to receive the keys
    const prev = statusRef.current;
    setSending(true);
    statusRef.current = "working";
    setStatus("working");
    const res = await apiJSON(`api/sessions/${q(session)}/input`, "POST", body).catch(() => null);
    const ok = !!res && !res.error;
    if (!ok) {
      statusRef.current = prev;
      setStatus(prev);
      toast(res?.error ? errText(res.error) : tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
    return ok;
  };

  // sendKeys drives the AskUserQuestion modal via named keys (Down/Space/Enter), the
  // only way to answer multi-select / multi-question forms (free text can't).
  const sendKeys = async (keys: string[]): Promise<boolean> => {
    if (!keys || !keys.length) return false;
    return await driveInput({ keys });
  };

  // sendSeq drives the modal with an ORDERED mix of named keys and literal text — the
  // path for answering a question via its "Type something" free-text row (move down to
  // it, type, Enter). Built by PendingQuestions.submit for multi-question / multi-select
  // forms where free text and option navigation are interleaved.
  const sendSeq = async (seq: Array<{ k?: string; t?: string }>): Promise<boolean> => {
    if (!seq || !seq.length) return false;
    return await driveInput({ seq });
  };

  // sendInterrupt stops the running turn — the equivalent of turn/interrupt, which under tui
  // becomes Escape (opencode's sub-agent detail-view special case is handled server-side in
  // /turn). The next poll resyncs the real state, so no optimistic state change is needed.
  //
  // It neither checks nor sets `sending`: a stop must stay pressable while an earlier stop is
  // still in flight, because on a Managed session that is exactly when the second stop — the
  // one that ends what the queue started — is needed (ADR 0105 decision 2). discardQueue is the
  // menu's "stop and discard the queue" (decision 3), which only a Managed session offers.
  const sendInterrupt = async (discardQueue = false) => {
    if (wsDown()) return; // WS stopped: no live turn to interrupt (also plan-reject / question-cancel)
    // An explicit stop (also plan-reject / question-cancel) means the user does NOT expect
    // a reply to render, so disarm the idle→reply bridge — otherwise the spinner would
    // linger over an interrupted, reply-less turn until the grace lapsed.
    wasWorkingRef.current = false;
    finalizingRef.current = false;
    setFinalizing(false);
    const res = await sessionInterrupt(session, discardQueue && managed);
    if (!res.ok) toast(res.message || tr("mirror.stop_failed"));
    // A first stop lets the queue go on, which looks like the stop did nothing unless said.
    else if (managed && res.stop === "first" && queuedCount > 0) toast(tr("mirror.stop_first_continues"));
    setTimeout(() => tickRef.current?.(), 400);
  };

  // cancelQuestion declines a Managed session's pending question (see the QuestionCard's
  // onCancel). Failures speak, as in sendRespond: silence would leave the card looking dead.
  const cancelQuestion = async (id: string) => {
    if (wsDown()) return;
    const res = await sessionCancelInteraction(session, id);
    if (!res.ok) toast(res.message || tr("mirror.answer_send_failed"));
    setTimeout(() => tickRef.current?.(), 400);
  };

  // An echo whose input a discard threw away never lands, so it is swept when the discard is
  // first seen (withoutDiscarded / sweptDiscards). Waits for stateSession: on the commit where
  // `session` changes, `discards` still belongs to the session being left.
  useEffect(() => {
    if (stateSession !== session || !discards.length) return;
    let swept = sweptDiscards.get(session);
    if (!swept) sweptDiscards.set(session, (swept = new Set()));
    const fresh = discards.filter((d) => !swept!.has(d.id));
    if (!fresh.length) return;
    for (const d of fresh) swept.add(d.id);
    const texts = fresh.flatMap((d) => d.items.map((i) => i.text));
    applyEchoes((p) => withoutDiscarded(p, texts));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [discards, session, stateSession]);

  // The input box is taken: putting queued or discarded text there would overwrite it.
  const draftBusy = !!draft.trim() || attachments.length > 0;

  // intoDraft puts a queued or discarded input back into the composer. It is never sent from
  // here: sending it again is the member's own act, and a new send gets a new message id —
  // the old entry's id is never reused, or a driver that records ids at accept time would
  // drop the resend as a duplicate (decision 5).
  const intoDraft = (item: QueueItem) => {
    setHistIdx(null);
    setDraft(item.text);
    if (item.attachments?.length) {
      attach.revive(
        item.attachments.map(
          (p): Attachment => ({ id: "", name: p.split("/").pop() || p, type: "", image: false, path: p, url: "" }),
        ),
      );
    }
    inputRef.current?.focus();
  };

  // takeQueued removes one still-queued entry (decision 5) and, for "back to input", puts it
  // into the composer — only once the removal succeeded, so the text is never both queued and
  // in the draft. already_started is the normal loss of a race with the pump, not an error.
  const takeQueued = async (id: string, restore: boolean) => {
    if (wsDown()) return;
    if (restore && draftBusy) {
      toast(tr("mirror.queued_restore_busy"));
      return;
    }
    if (queueOpsRef.current.has(id)) return;
    queueOpsRef.current.add(id);
    const res = await sessionRemoveQueued(session, id);
    queueOpsRef.current.delete(id);
    if (res.ok) {
      const gone = res.removed;
      if (gone) {
        // The optimistic echo of this input would otherwise wait forever for a turn that will
        // never come.
        const text = gone.text.trim();
        applyEchoes((p) => {
          const i = p.findIndex((e) => e.text.trim() === text);
          return i < 0 ? p : [...p.slice(0, i), ...p.slice(i + 1)];
        });
        // The bubble offers "back to input" on member input only; this keeps a stale bubble
        // from putting a peer's envelope into the draft all the same (decision 4).
        if (restore && isMemberOrigin(gone.origin)) intoDraft(gone);
      }
    } else if (res.code === "already_started") toast(tr("mirror.queued_already_started"));
    else if (res.code === "not_queued") toast(tr("mirror.queued_gone"));
    else toast(res.message || tr("mirror.send_failed"));
    setTimeout(() => tickRef.current?.(), 250);
  };

  // Drops one waiting peer message (#1031). Hidden at once; the next poll confirms.
  const dropPendingPeer = async (id: string) => {
    if (wsDown()) return;
    setPendingPeers((p) => p.filter((x) => x.id !== id));
    const res = await sessionDropPendingPeer(session, id);
    if (!res.ok) toast(res.code === "not_pending" ? tr("mirror.peer_pending_gone") : res.message || tr("mirror.send_failed"));
    setTimeout(() => tickRef.current?.(), 250);
  };

  // The discard notice's two actions (decision 4). Restoring the last member entry and a close
  // both tell the driver to drop the discard, so other tabs stop offering it; a failure there only
  // leaves it offered elsewhere, and nothing is ever sent twice, so it is not reported.
  const restoreDiscard = (d: Discard) => {
    if (draftBusy) {
      toast(tr("mirror.discarded_restore_busy"));
      return;
    }
    const step = restoreStep(discardNotices, d);
    if (!step.item) return;
    setDiscardNotices(step.next);
    if (step.dismiss) void sessionDismissDiscard(session, d.id);
    intoDraft(step.item);
  };
  const closeDiscard = (id: string) => {
    const step = closeStep(discardNotices, id);
    setDiscardNotices(step.next);
    if (step.dismiss) void sessionDismissDiscard(session, id);
  };

  // sendApproval answers a MANAGED session's pending tool approval. It mirrors sendRespond,
  // including the rollback: a rejection must leave the card alive rather than clear it, because
  // the tool is still blocked and the member would have no way back to it.
  const sendApproval = async (id: string, allow: boolean): Promise<boolean> => {
    if (sending) return false;
    if (wsDown()) return false;
    setSending(true);
    const prev = statusRef.current;
    statusRef.current = "working";
    setStatus("working");
    const res = await sessionApprove(session, id, allow).catch((): TurnResult => ({ ok: false }));
    if (!res.ok) {
      statusRef.current = prev;
      setStatus(prev);
      toast(res.message || tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
    return res.ok;
  };

  // sendRespond answers a MANAGED session's pending question by interaction id —
  // a structured answer (docs/log/27 §5). A tui question is still answered by navigating the
  // TUI modal with sendKeys/sendSeq; the server rejects /respond for tui anyway.
  const sendRespond = async (id: string, answers: InteractionAnswer[]): Promise<boolean> => {
    if (sending) return false;
    if (wsDown()) return false; // WS stopped: the managed session's structured answer can't be delivered
    setSending(true);
    const prev = statusRef.current;
    statusRef.current = "working";
    setStatus("working");
    const res = await sessionRespond(session, id, answers).catch((): TurnResult => ({ ok: false }));
    if (!res.ok) {
      // Never swallow a rejection (unknown id, driver not implemented, connection lost):
      // roll the status back, keep the question card alive and show the reason if there is
      // one. The next poll resyncs the real state.
      statusRef.current = prev;
      setStatus(prev);
      toast(res.message || tr("mirror.answer_send_failed"));
    }
    setSending(false);
    setTimeout(() => tickRef.current?.(), 400);
    return res.ok;
  };

  return {
    postInput, wsDown, postKeys, newestIdx, sendPrompt, driveInput, sendKeys, sendSeq, sendInterrupt,
    cancelQuestion, draftBusy, intoDraft, takeQueued, dropPendingPeer, restoreDiscard, closeDiscard,
    sendApproval, sendRespond,
  };
}

export type MirrorActions = ReturnType<typeof useMirrorActions>;
