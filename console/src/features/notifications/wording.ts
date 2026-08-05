import { t } from "../../lib/i18n/index.ts";

// Notification wording is deliberately browser-state free: the center, desktop
// delivery, TTS replay, and node tests must all resolve the same localized text.
export interface NotificationWordingInput {
  kind: string;
  displayName: string;
  payload: Record<string, unknown>;
}

export function notificationWording(n: NotificationWordingInput): { title: string; body: string; speech: string } {
  const name = n.displayName || t("notif.default_name");
  if (n.kind === "answer-ready") return { title: t("notif.answer_ready.title"), body: name, speech: t("notif.answer_ready.speech", { name }) };
  if (n.kind === "question") return { title: t("notif.question.title"), body: name, speech: t("notif.question.speech", { name }) };
  if (n.kind === "plan-approval") return { title: t("notif.plan_approval.title"), body: name, speech: t("notif.plan_approval.speech", { name }) };
  if (n.kind === "permission-request") return { title: t("notif.permission_request.title"), body: name, speech: t("notif.permission_request.speech", { name }) };
  if (n.kind === "session-report") {
    // A session reported back to its operator conversation (docs/30); the body names
    // the reporting session, the click target is the conversation.
    return { title: t("notif.session_report.title"), body: name, speech: t("notif.session_report.speech", { name }) };
  }
  if (n.kind === "chat-auto-paused") {
    // The operator's unattended auto-turn budget ran out and the loop paused (docs/30).
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
  const rawSource = String(n.payload.source || n.displayName || "AI");
  const source = rawSource === "claude" ? "Claude" : rawSource === "codex" ? "Codex" : rawSource;
  const win = n.payload.windowKey === "5h" ? t("notif.window.5h") : t("notif.window.week");
  return { title: t("notif.usage_reset.title", { source }), body: t("notif.usage_reset.body", { window: win }), speech: t("notif.usage_reset.speech", { source, window: win }) };
}
