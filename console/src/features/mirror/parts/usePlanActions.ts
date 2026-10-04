import { useEffect } from "react";
import { sessionPlanFile, sessionPlanRespond } from "../../../core/api/client.ts";
import type { Session } from "../../../types/session.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { useLayoutStore } from "../../../layout/store.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { useLaunchSeed, useLaunchTarget, useReposStore } from "../../repos/store.ts";
import { handoffLaunchTarget } from "../handoffLaunch.ts";
import { deliverPlanComments, planKey } from "../planComments.ts";
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
    applyEchoes, setFinalizing, finalizingRef, wasWorkingRef, alive, pendingPlan, markRejected, sending,
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
    // inPlace: the reviewer reads the working copy this plan is about, uncommitted work
    // included. A worktree would show it the base instead — the dialog still offers one.
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

  return { openPlan, reviewPlanElsewhere, planSendBlocked, sendPlanComments, openFile, openDiff };
}
