import { t, type MsgKey } from "../../lib/i18n/index.ts";

// Row headings for the notification center. Every kind wording() handles must appear here too: a
// missing kind renders the raw identifier (`handoff-offer`) in the row instead of a translation,
// and the types cannot catch the omission. wording.test.ts therefore scrapes the kinds out of
// *this file's source* and checks them against the table.
export const NOTIFICATION_KIND_LABELS: Record<string, MsgKey> = {
  "answer-ready": "noti.kind_answer_ready",
  question: "noti.kind_question",
  "plan-approval": "noti.kind_plan_approval",
  "permission-request": "noti.kind_permission_request",
  "usage-reset": "noti.kind_usage_reset",
  "session-report": "noti.kind_session_report",
  "chat-auto-paused": "noti.kind_chat_auto_paused",
  "chat-context-pressure": "noti.kind_chat_context_pressure",
  "chat-context-overflow": "noti.kind_chat_context_overflow",
  "rate-limit-reached": "noti.kind_rate_limit_reached",
  "rate-limit-resumed": "noti.kind_rate_limit_resumed",
  "submodule-sync": "noti.kind_submodule_sync",
  "schedule-failed": "noti.kind_schedule_failed",
  "schedule-skipped": "noti.kind_schedule_skipped",
  "carried-interaction": "noti.kind_carried_interaction",
  "handoff-offer": "noti.kind_handoff_offer",
  "handoff-accepted": "noti.kind_handoff_accepted",
  "handoff-expired": "noti.kind_handoff_expired",
  "arch-residue": "noti.kind_arch_residue",
};

/** Translated row heading; only an unknown kind (new CP with an old Console) falls back to the raw identifier. */
export function notificationKindLabel(kind: string): string {
  const key = NOTIFICATION_KIND_LABELS[kind];
  return key ? t(key) : kind;
}

// notificationRowSubtitle is the subtitle of a notification-center row. It defaults to
// displayName (for most kinds, the session's display name), but a session report's destination is
// a conversation, so it shows "who -> which conversation". Without the conversation name the row
// does not say which conversation the report was for, and clicking is the only way to find out.
export function notificationRowSubtitle(n: NotificationWordingInput): string {
  const conv = typeof n.payload.conversationTitle === "string" ? n.payload.conversationTitle : "";
  if (n.kind === "session-report" && conv) return `${n.displayName} → ${conv}`;
  return n.displayName;
}

// Notification wording is deliberately browser-state free: the center, desktop
// delivery, TTS replay, and node tests must all resolve the same localized text.
export interface NotificationWordingInput {
  kind: string;
  displayName: string;
  payload: Record<string, unknown>;
}

// scheduleFailureReason turns the ledger's short status token into something a reader can
// act on. The soft skips have a fixed set of causes, each with its own next step; a hard
// failure carries the actual message after "error:" and that text IS the answer to "why
// didn't it run", so it is passed through rather than flattened to a generic sentence.
function scheduleFailureReason(status: string): string {
  switch (status) {
    case "skipped_quota":
      return t("notif.schedule.reason_quota");
    case "skipped_rate_limited":
      return t("notif.schedule.reason_rate_limited");
    case "skipped_membership_inactive":
      return t("notif.schedule.reason_membership");
    case "skipped_target_missing":
      return t("notif.schedule.reason_target_missing");
    case "skipped_overlap":
      return t("notif.schedule.reason_overlap");
  }
  return status.startsWith("error:") ? status.slice("error:".length).trim() : "";
}

export function notificationWording(n: NotificationWordingInput): { title: string; body: string; speech: string } {
  const name = n.displayName || t("notif.default_name");
  if (n.kind === "answer-ready") return { title: t("notif.answer_ready.title"), body: name, speech: t("notif.answer_ready.speech", { name }) };
  if (n.kind === "question") return { title: t("notif.question.title"), body: name, speech: t("notif.question.speech", { name }) };
  if (n.kind === "plan-approval") return { title: t("notif.plan_approval.title"), body: name, speech: t("notif.plan_approval.speech", { name }) };
  if (n.kind === "permission-request") return { title: t("notif.permission_request.title"), body: name, speech: t("notif.permission_request.speech", { name }) };
  if (n.kind === "session-report") {
    // A session reported back to its operator conversation (docs/log/30); the body names
    // the reporting session, the click target is the conversation.
    return { title: t("notif.session_report.title"), body: name, speech: t("notif.session_report.speech", { name }) };
  }
  if (n.kind === "chat-auto-paused") {
    // The operator's unattended auto-turn budget ran out and the loop paused (docs/log/30).
    // The body names the conversation; clicking opens it so the user can reply to resume.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_auto_paused.title"), body: title, speech: t("notif.chat_auto_paused.speech") };
  }
  if (n.kind === "chat-context-pressure") {
    // A conversation crossed the context-fill threshold (chat_usage.go) — matters even
    // when it happened on an unattended auto turn; clicking opens the conversation.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_ctx_pressure.title"), body: title, speech: t("notif.chat_ctx_pressure.speech") };
  }
  if (n.kind === "chat-context-overflow") {
    // A turn failed on context overflow and couldn't be auto-healed (chat_recover.go);
    // clicking opens the conversation so the user can compact or start fresh.
    const title = String(n.payload.conversationTitle || name);
    return { title: t("notif.chat_ctx_overflow.title"), body: title, speech: t("notif.chat_ctx_overflow.speech") };
  }
  if (n.kind === "submodule-sync") {
    // A working copy was launched with submodules that are not checked out — the fetch is
    // still running, or it failed (git_submodule.go). Naming the paths matters: the session
    // sees empty directories and git reports nothing wrong.
    const repo = String(n.payload.repo || name);
    if (n.payload.state === "ready") {
      return { title: t("notif.submodules_ready.title"), body: repo, speech: t("notif.submodules_ready.speech", { repo }) };
    }
    const paths = Array.isArray(n.payload.paths) ? n.payload.paths.map(String).join(", ") : "";
    return {
      title: t("notif.submodules_incomplete.title"),
      body: t("notif.submodules_incomplete.body", { repo, paths }),
      speech: t("notif.submodules_incomplete.speech", { repo }),
    };
  }
  if (n.kind === "carried-interaction") {
    // A session was folded away while still holding an interaction waiting for an answer
    // (docs/log/75). Folding is harmless in itself (the interaction is carried over, not lost),
    // but the user does not know that: the list badge is only visible to someone with the Console
    // open, and the original question notification only said "please answer". So this says the
    // interaction is still pending, and where it can be answered.
    const kindText =
      n.payload.interaction === "plan"
        ? t("notif.carried.plan")
        : n.payload.interaction === "permission"
          ? t("notif.carried.permission")
          : t("notif.carried.question");
    return {
      title: t("notif.carried.title", { what: kindText }),
      body: t("notif.carried.body", { name }),
      speech: t("notif.carried.speech", { name, what: kindText }),
    };
  }
  if (n.kind === "handoff-offer") {
    // A handoff arrived from another member (docs/log/77); the body is the handoff's display name
    // and clicking goes to the shared view.
    return { title: t("notif.handoff_offer.title"), body: name, speech: t("notif.handoff_offer.speech") };
  }
  if (n.kind === "handoff-accepted") {
    return { title: t("notif.handoff_accepted.title"), body: name, speech: t("notif.handoff_accepted.speech") };
  }
  if (n.kind === "handoff-expired") {
    // It expired without being accepted. No reason is asked for, but the fact that it is now left
    // hanging has to be reported.
    return { title: t("notif.handoff_expired.title"), body: name, speech: t("notif.handoff_expired.speech") };
  }
  if (n.kind === "schedule-failed" || n.kind === "schedule-skipped") {
    // A scheduled run did not happen (docs/log/38). Without this branch, schedule-* fell through
    // to the usage-reset wording at the end of the function, so the center, the OS notification
    // and the narration all announced a usage-limit reset: the notification arrived, but about a
    // different event. Falling through to the last branch is the structure itself, so every new
    // kind must be added here as well.
    const label = String(n.payload.spec_label || name);
    const reason = scheduleFailureReason(String(n.payload.status || ""));
    const title = n.kind === "schedule-failed" ? t("notif.schedule_failed.title") : t("notif.schedule_skipped.title");
    const speech =
      n.kind === "schedule-failed"
        ? t("notif.schedule_failed.speech", { name: label })
        : t("notif.schedule_skipped.speech", { name: label });
    return { title, body: reason ? t("notif.schedule.body_reason", { name: label, reason }) : label, speech };
  }
  if (n.kind === "rate-limit-reached") {
    return {
      title: t("notif.rate_limit_reached.title"),
      body: t("notif.rate_limit_reached.body", { name }),
      speech: t("notif.rate_limit_reached.speech", { name }),
    };
  }
  if (n.kind === "rate-limit-resumed") {
    return {
      title: t("notif.rate_limit_resumed.title"),
      body: t("notif.rate_limit_resumed.body", { name }),
      speech: t("notif.rate_limit_resumed.speech", { name }),
    };
  }
  if (n.kind === "arch-residue") {
    // The CPU architecture changed and some artefacts could not be restored automatically
    // (docs/decisions/0068). The subject is the workspace, not a session, so clicking leads
    // nowhere: the row's whole job is to name *what* is left, with enough detail for the reader
    // to fix it from a terminal.
    const repos = Array.isArray(n.payload.repos) ? (n.payload.repos as unknown[]).map(String) : [];
    const bins = Array.isArray(n.payload.bins) ? (n.payload.bins as unknown[]).map(String) : [];
    const items = [...repos, ...bins];
    const body = items.length ? items.join(", ") : t("notif.arch_residue.body_generic");
    return { title: t("notif.arch_residue.title"), body, speech: t("notif.arch_residue.speech") };
  }
  const rawSource = String(n.payload.source || n.displayName || "AI");
  const source = rawSource === "claude" ? "Claude" : rawSource === "codex" ? "Codex" : rawSource;
  const win = n.payload.windowKey === "5h" ? t("notif.window.5h") : t("notif.window.week");
  return { title: t("notif.usage_reset.title", { source }), body: t("notif.usage_reset.body", { window: win }), speech: t("notif.usage_reset.speech", { source, window: win }) };
}
