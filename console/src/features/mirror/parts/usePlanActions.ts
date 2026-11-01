import { createElement, useEffect, useRef, useState } from "react";
import { sessionPlanFile, sessionPlanRespond } from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { useLayoutStore } from "../../../layout/store.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { dismissToast } from "../../../ui/toast.ts";
import { useSessionsStore } from "../../sessions/store.ts";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../../repos/store.ts";
import { handoffLaunchTarget } from "../handoffLaunch.ts";
import { discardCarriedAfterReview } from "../carriedPlanReview.ts";
import { deliverPlanComments, planKey } from "../planComments.ts";
import {
  AUTO_REVIEW_DEFAULT_ROUNDS, liveReviewer, rememberReviewer, reviewerLabel, runRereview, setReviewAuto, useHasRememberedReviewer, useReviewAuto, useReviewer,
} from "../reviewAgain.ts";
import { reviewPrompt, reviewTitle } from "../planReview.ts";
import { planTitle } from "../transcript/blocks.tsx";
import type { Part } from "../transcript/types.ts";
import { nextEchoId } from "./sendEcho.ts";
import { findDiffPane, findPane, findPlanPane } from "./panes.ts";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";

type LayoutState = ReturnType<typeof useLayoutStore.getState>;

/**
 * usePlanActions opens what the transcript points at in panes (a plan, a file, an edit diff)
 * and carries the plan card's own sends: comments and "review elsewhere". The plan-follow
 * effect keeps an open review pane on the plan text currently proposed.
 */
export function usePlanActions({
  session,
  sessionMeta,
  running,
  readOnly,
  toast,
  st,
  actions,
  openTargetInNew,
  setPaneTarget,
  setActivePane,
}: {
  session: string;
  sessionMeta?: Session | null;
  running: boolean;
  readOnly: boolean;
  toast: ReturnType<typeof useToast>;
  st: MirrorState;
  actions: MirrorActions;
  openTargetInNew: LayoutState["openTargetInNew"];
  setPaneTarget: LayoutState["setPaneTarget"];
  setActivePane: LayoutState["setActive"];
}) {
  const {
    applyEchoes, setFinalizing, finalizingRef, wasWorkingRef, alive, pendingPlan, markRejected, sending, carried, setCarried,
    setSending, tickRef,
  } = st;
  const { wsDown, sendInterrupt, sendPrompt, newestIdx } = actions;

  // Open a plan's Markdown in its own pane (manual — via a button, not automatic).
  // The pane carries docSession so it becomes a REVIEW surface (select → comment);
  // the comments are keyed by session + plan text, which is what makes the plan card
  // able to collect them again.
  //
  // Why not plain showDoc: doc panes are identified by TITLE alone (layout/ops
  // sameTarget), so a revised plan re-presented under the same heading would just
  // FOCUS the pane still showing the OLD text — and comments would then be written
  // against text the agent no longer proposes. Replace the content of an already-open
  // plan pane for this session instead, and fall back to opening a new one.
  const openPlan = (plan: string) => {
    const target = {
      content: { kind: "doc" as const, docTitle: planTitle(plan), docContent: plan, docSession: session },
    };
    const open = findPlanPane(session);
    if (open) {
      setPaneTarget(open, target);
      setActivePane(open);
      return;
    }
    openTargetInNew(target);
  };

  // The plan file the cards' "review again" is keyed by: fetched once per presented plan text,
  // and only for a planner that has a remembered reviewer at all — the endpoint snapshots the
  // plan into a new file when claude recorded no path, which is not worth doing for every
  // plan card on screen. A snapshot path changes with every revision, so it never keys anything.
  const planText = pendingPlan || (carried?.kind === "plan" ? carried.plan || "" : "");
  const [planPath, setPlanPath] = useState("");
  const hasMemory = useHasRememberedReviewer(session);
  useEffect(() => {
    setPlanPath("");
    if (!planText || !hasMemory) return;
    let stale = false;
    void sessionPlanFile(session).then((r) => {
      if (!stale && r.ok && r.path && r.source === "claude") setPlanPath(r.path);
    });
    return () => {
      stale = true;
    };
  }, [planText, session, hasMemory]);
  const reviewer = useReviewer(session, planPath);
  const rereview = reviewer ? { name: reviewer.name, label: reviewerLabel(reviewer) } : null;
  // "Review automatically, up to N rounds" (#2810). The Agent runs the loop; this edits its record.
  // Offered only when the Agent already holds the link (a link seen only by this browser would be
  // refused as no_link) (planPath is only set for a plan file claude wrote).
  const autoState = useReviewAuto(session, planPath);
  const linkedAtAgent = useSessionsStore((s) => Array.isArray(s.sessions.find((x) => x.name === session)?.planReviewers));
  const reviewAuto =
    reviewer && planPath && linkedAtAgent
      ? {
          enabled: !!autoState?.enabled,
          max: autoState?.max || AUTO_REVIEW_DEFAULT_ROUNDS,
          round: autoState?.round ?? 0,
          stop: autoState?.stop || "",
          onChange: (enabled: boolean, max: number) => {
            void setReviewAuto(session, planPath, reviewer.name, enabled, max).then((code) => {
              if (code) toast(tr("plan.auto_failed", { code }));
            });
          },
        }
      : null;

  // Remember who reviews this plan once the launch dialog has created the session. Only a path
  // claude wrote keys anything (see above); a snapshot is a one-off copy.
  const rememberOnLaunch = (res: { path?: string; source?: string }) => (name: string) => {
    if (name && res.path && res.source === "claude") rememberReviewer(session, res.path, name);
  };

  // The reviewer to send a revised plan to, checked again at press time (the list moves on
  // while a card sits on screen). Null = take the launch dialog instead.
  const reviewerNow = async (): Promise<{ path: string; reviewer: Session } | null> => {
    const res = await sessionPlanFile(session);
    if (!res.ok || !res.path || res.source !== "claude") return null;
    const r = liveReviewer(session, res.path, useSessionsStore.getState().sessions);
    return r ? { path: res.path, reviewer: r } : null;
  };

  // Review this plan in another session: reject it, then open the ordinary launch dialog
  // seeded with a prompt that points the reviewer at the plan FILE.
  //
  // The order is forced. The findings come back as a peer message, and a session waiting on
  // plan approval refuses free text of every kind (409 plan_pending) — because the approval
  // modal would swallow the text and turn its Enter into an approval of the plan under
  // review. Rejected, the planner sits idle in plan mode and the message lands.
  //
  // The path is fetched BEFORE anything is rejected: a launch with no plan to review is
  // worse than no launch, and this way a failure leaves the plan exactly as it was.
  const reviewPlanElsewhere = async (plan: string) => {
    retireRetry(); // a different review starts: an older "send again" must not fire after it
    if (planSendBlocked) {
      toast(planSendBlocked);
      return;
    }
    if (wsDown()) return;
    const res = await sessionPlanFile(session);
    if (!res.ok || !res.path) {
      toast(tr("plan.review_launch_failed", { err: res.message || "" }));
      return;
    }
    const target = handoffLaunchTarget(sessionMeta, useReposStore.getState().repos, false);
    if ("error" in target) {
      toast(tr(target.error === "no_parent" ? "mirror.handoff_no_parent" : "mirror.handoff_no_dir"));
      return;
    }
    markRejected(plan, true); // optimistic "rejected" badge; planOutcome reconciles it
    wasWorkingRef.current = false; // as with an interrupt: no reply is being waited for
    await sendInterrupt();
    // Only the prompt and the title: the lineage fields belong to a handoff PROPOSAL, and
    // StartHost would try to badge one that does not exist.
    useLaunchSeed.getState().set(
      reviewPrompt({ parent: session, path: res.path, dir: target.repo.path || "" }),
      reviewTitle(planTitle(plan)),
    );
    useLaunchSeed.getState().setOnLaunched(rememberOnLaunch(res));
    // inPlace: the reviewer reads the working copy this plan is about, uncommitted work
    // included. A worktree would show it the base instead — the dialog still offers one.
    useLaunchTarget.getState().open(target.repo, "", true);
  };

  // One "review again" at a time per card: a press holds this from before its first await until
  // it settles (the resume alone can take 30 s), and the cards disable their other buttons on
  // `rereviewing`. Without it a second press, an approval or a launch overlaps the first, and a
  // late interrupt lands on a planner the member has already approved.
  const rereviewLock = useRef({ held: false });
  const [rereviewing, setRereviewing] = useState(false);
  const latest = useRef({ pending: "", carried: "" });
  latest.current = { pending: pendingPlan || "", carried: carried?.kind === "plan" ? carried.plan || "" : "" };
  // Bumped by anything that makes an outstanding "send again" toast stale: a new review-again or
  // launch, a new plan presented, leaving the card. The toast's retry refuses on a mismatch.
  const retryEpoch = useRef(0);
  const retryKey = `plan-rereview:${session}`;
  const retireRetry = () => {
    retryEpoch.current++;
    dismissToast(retryKey);
  };
  useEffect(() => retireRetry, []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (pendingPlan) retireRetry();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingPlan]);
  const exclusive = async (fn: () => Promise<unknown>) => {
    if (rereviewLock.current.held) return;
    retireRetry();
    rereviewLock.current.held = true;
    setRereviewing(true);
    try {
      await fn();
    } finally {
      rereviewLock.current.held = false;
      setRereviewing(false);
    }
  };

  // A failure after the live card's reject has no card left to press again, so the toast carries
  // the retry (the send alone: the planner is already rejected).
  const rereviewFailed = (r: Session, err: string, retry?: () => Promise<boolean>) => {
    const msg = tr("plan.review_again_failed", { name: reviewerLabel(r), err });
    if (!retry) {
      toast(msg);
      return;
    }
    toast(
      createElement(
        "span",
        { className: "update-toast" },
        createElement("span", { className: "update-toast-txt" }, msg),
        createElement("button", { type: "button", className: "update-toast-btn", onClick: () => void retry() }, tr("plan.review_again_retry")),
      ),
      { kind: "error", duration: 0, key: retryKey }, // same key: a failed retry replaces this toast
    );
  };

  // "Review again in <reviewer>" on the live card: the same ordering as reviewPlanElsewhere
  // (the planner must be idle in plan mode, not behind the approval modal, when the peer message
  // arrives), with one addition — the reviewer is made ready FIRST, so a reviewer that will not
  // come up leaves the plan untouched. An interrupt that did not go through stops the send and
  // takes the optimistic "rejected" back.
  const reviewPlanAgain = (plan: string) =>
    exclusive(async () => {
      if (planSendBlocked) {
        toast(planSendBlocked);
        return;
      }
      if (wsDown()) return;
      const hit = await reviewerNow();
      if (!hit) return reviewPlanElsewhere(plan); // reviewer deleted since the card rendered
      await runRereview({
        planner: session,
        path: hit.path,
        plan,
        reviewer: hit.reviewer,
        lock: { held: false },
        stillCurrent: () => latest.current.pending.trim() === plan.trim(),
        beforeSend: async () => {
          markRejected(plan, true); // optimistic "rejected" badge; planOutcome reconciles it
          wasWorkingRef.current = false; // as with an interrupt: no reply is being waited for
          const ok = await sendInterrupt();
          if (!ok) markRejected(plan, false);
          return ok;
        },
        onFail: (err, retry) => rereviewFailed(hit.reviewer, err, retry),
        retryValid: ((epoch) => () => retryEpoch.current === epoch && !latest.current.pending)(retryEpoch.current),
        onRetryDone: () => dismissToast(retryKey),
      });
    });

  // The same from the carried card: nothing to reject, and the card is discarded only after the
  // send succeeded — a failed send leaves the plan reachable.
  const reviewCarriedPlanAgain = (plan: string) =>
    exclusive(async () => {
      const hit = await reviewerNow();
      if (!hit) return reviewCarriedPlan(plan);
      await runRereview({
        planner: session,
        path: hit.path,
        plan,
        reviewer: hit.reviewer,
        lock: { held: false },
        stillCurrent: () => latest.current.carried === plan,
        onSent: discardCarriedAfterReview({ session, setCarried, toast }),
        onFail: (err) => rereviewFailed(hit.reviewer, err),
      });
    });

  // Review the plan a STOPPED session was waiting on (the carried plan card). The same
  // launch dialog, minus everything the live card needs: no modal is up, so nothing is
  // rejected or interrupted, and the session need not be running — the reviewer's peer
  // message resumes it.
  //
  // The carried card is discarded once a reviewer session has actually been created (not
  // when the dialog opens: a cancelled dialog must leave the plan reachable). Discarding is
  // server-side state, so no pane or reload can approve a plan whose review is under way,
  // and discard does not wake the session.
  const reviewCarriedPlan = async (plan: string) => {
    retireRetry();
    const res = await sessionPlanFile(session);
    if (!res.ok || !res.path) {
      toast(tr("plan.review_launch_failed", { err: res.message || "" }));
      return;
    }
    const target = handoffLaunchTarget(sessionMeta, useReposStore.getState().repos, false);
    if ("error" in target) {
      toast(tr(target.error === "no_parent" ? "mirror.handoff_no_parent" : "mirror.handoff_no_dir"));
      return;
    }
    useLaunchSeed.getState().set(
      reviewPrompt({ parent: session, path: res.path, dir: target.repo.path || "" }),
      reviewTitle(planTitle(plan)),
    );
    const discard = discardCarriedAfterReview({ session, setCarried, toast });
    const remember = rememberOnLaunch(res);
    useLaunchSeed.getState().setOnLaunched((name) => {
      remember(name);
      discard();
    });
    useLaunchTarget.getState().open(target.repo, "", true);
  };

  // Why plan comments cannot be sent ("" = they can). The composer disappears entirely while
  // stopped, but the plan card stays in the history, so its send button must block itself.
  // Collecting comments while stopped is still allowed — they go out after a resume.
  const planSendBlocked = !running
    ? tr("mirror.ws_stopped")
    : !alive || readOnly
      ? tr("plan.send_needs_running")
      : "";

  // When reject → revise → re-present replaces the plan text, follow it in the open review
  // pane too. Without this the reader comments against the old text and only discovers after
  // sending that the passage is gone — a doc pane is a snapshot and stays stale silently.
  useEffect(() => {
    if (!pendingPlan) return;
    const id = findPlanPane(session);
    if (!id) return;
    const pane = findPane(id);
    if (pane?.content.kind !== "doc" || pane.content.docContent === pendingPlan) return;
    setPaneTarget(id, {
      content: { kind: "doc", docTitle: planTitle(pendingPlan), docContent: pendingPlan, docSession: session },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingPlan, session]);

  // Deliver comments on a plan. Which route to send by, and when to mark them sent, is
  // decided by deliverPlanComments (planComments.ts): this component is too large to have a
  // rendering test, so the decision is lifted out and pinned by unit tests. What stays here
  // is the React housekeeping — press guard, optimistic "rejected" badge, toast, echo.
  const sendPlanComments = async (plan: string) => {
    if (sending) return;
    // Nothing reaches a stopped session. The plan card also renders in the history, so it
    // does not get the composer's "hidden while stopped" protection and must refuse here.
    // The button is disabled via planSendBlocked too — this is the second layer, for a press
    // that races the stop.
    if (planSendBlocked) {
      toast(planSendBlocked);
      return;
    }
    if (wsDown()) return;
    const isPending = !!pendingPlan && pendingPlan.trim() === plan.trim();
    if (isPending) {
      // The route that carries a rejection: block the send button and clear the badge and
      // awaiting-reply state up front. (sendPrompt manages `sending` itself, so the
      // speak-only route leaves it alone.)
      setSending(true);
      markRejected(plan, true); // optimistic "rejected" badge; planOutcome reconciles it
      wasWorkingRef.current = false; // as with an interrupt: no reply is being waited for
      finalizingRef.current = false;
      setFinalizing(false);
    }
    const res = await deliverPlanComments(planKey(session, plan), {
      pending: isPending,
      respond: (feedback) => sessionPlanRespond(session, "reject", feedback),
      say: (feedback) => sendPrompt(feedback),
    });
    if (isPending) setSending(false);
    if (!res) return; // nothing to send
    if (!res.ok) {
      // Undelivered means the comments were not folded away, so state the reason and make it
      // clear they can be re-sent (undelivered = the rejection went through but the text did
      // not). A failure on the say route is not re-toasted: sendPrompt has already given the
      // concrete reason (awaiting permission, stopped, …) and a generic "send failed" on top
      // of it would obscure what happened.
      if (res.reason !== "say") {
        toast(res.message || tr(res.reason === "undelivered" ? "plan.feedback_undelivered" : "mirror.send_failed"));
      }
      return;
    }
    if (res.via === "reject") {
      const echoId = nextEchoId(); // optimistic echo until the real turn lands (as in sendPrompt)
      applyEchoes((p) => [...p, { id: echoId, text: res.feedback, sinceIdx: newestIdx(), at: Date.now() }]);
      setTimeout(() => tickRef.current?.(), 400);
    }
  };

  // Open a SendUserFile entry in its own split pane (same as the file tree's split-open).
  const openFile = (path: string, line?: number, column?: number) =>
    openTargetInNew(
      { content: { kind: "file", filePath: path, targetLine: line, targetColumn: column } },
      true,
    );

  // Open an edit trace's captured before/after in a diff pane. The mirror HAS panes, so
  // it must pass this capability: without it ToolTrace silently takes the degraded path
  // meant for the pane-less shared view (transcript/capabilities.ts) and an edit becomes
  // an inline expansion with nothing to open — which is how it behaved until docs/log/68.
  const openDiff = (p: Part) => {
    const title = p.file ? p.file.split("/").pop() || p.file : p.tool || tr("view.diff");
    const target = { content: { kind: "diff" as const, docTitle: title, diffTool: p.tool || "", diffEdits: p.edits || [] } };
    const open = findDiffPane();
    if (open) {
      setPaneTarget(open, target);
      setActivePane(open);
      return;
    }
    openTargetInNew(target, true);
  };

  return { openPlan, reviewPlanElsewhere, reviewCarriedPlan, reviewPlanAgain, reviewCarriedPlanAgain, rereview, reviewAuto, rereviewing, planSendBlocked, sendPlanComments, openFile, openDiff };
}
