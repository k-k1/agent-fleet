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
    // 答えを待っていた対話を抱えたままセッションが畳まれた（docs/log/75）。畳むこと自体は
    // 無害（持ち越してあるので失われない）が、利用者はそれを知らない — 一覧のバッジは
    // Console を開いている人にしか見えず、質問時の通知は「答えてください」としか言って
    // いない。だから「まだ保留のままだ」と、どこで答えられるかを言う。
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
    // 別メンバーから引き継ぎが届いた（docs/log/77）。body は引き継ぎの表示名で、遷移先は共有ビュー。
    return { title: t("notif.handoff_offer.title"), body: name, speech: t("notif.handoff_offer.speech") };
  }
  if (n.kind === "handoff-accepted") {
    return { title: t("notif.handoff_accepted.title"), body: name, speech: t("notif.handoff_accepted.speech") };
  }
  if (n.kind === "handoff-expired") {
    // 受領されないまま失効した。理由は求めない代わりに「宙に浮いた」ことだけは知らせる。
    return { title: t("notif.handoff_expired.title"), body: name, speech: t("notif.handoff_expired.speech") };
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
