// English catalog. Typed as Record<keyof typeof ja, string> so tsc fails the build on any
// missing OR extra key — this is our completeness guard in place of a library's tooling.
import type { ja } from "./ja.ts";

export const en: Record<keyof typeof ja, string> = {
  // --- common / settings labels ---
  "settings.language": "Language",
  "theme.dark": "Dark",
  "theme.light": "Light",
  "region_theme.inherit": "Match app",

  // --- API errors (mirror of ERR_TEXT + inline fallbacks) ---
  "err.ssm_search_forbidden":
    "You don't have permission to search AWS instances. Ask your AWS administrator to grant ssm:DescribeInstanceInformation.",
  "err.quota_sessions":
    "You've reached the limit on concurrently running sessions. Stop one of the running sessions before creating another.",
  "err.sessions_running":
    "This working copy has running sessions. Switching would swap and break the working tree underfoot, so it's blocked here. Open the branch as a separate working copy instead.",
  "err.sessions_running_delete":
    "This working copy has running sessions. Deleting would remove the working directory underfoot and break them, so stop those sessions first.",
  "err.worktree_dirty":
    "This worktree has uncommitted/unpushed changes. Force-deleting it will lose them.",
  "err.has_worktrees":
    "This working copy has derived worktrees attached to it. Delete the worktrees first.",
  "err.worktree_remove_failed": "Failed to remove the worktree.",
  "err.question_pending":
    "The agent is waiting for an answer to its question. Answer it from the question card before sending.",
  "err.not_running": "The session is stopped. Resume it before sending.",
  "err.driver_unavailable": "The managed driver isn't available yet for this agent type.",
  "err.runtime_failed": "Couldn't connect to the agent's shared runtime.",
  "err.send_failed": "Failed to send.",
  "err.network": "Network error",
  "err.settings_change_failed": "Couldn't change the setting.",

  // --- notifications (speech is the spoken variant read by TTS) ---
  "notif.default_name": "Session",
  "notif.answer_ready.title": "A reply is ready",
  "notif.answer_ready.speech": "{name} has replied.",
  "notif.question.title": "A question is waiting",
  "notif.question.speech": "{name} is asking for confirmation.",
  "notif.plan_approval.title": "A plan is awaiting approval",
  "notif.plan_approval.speech": "{name} is asking you to approve a plan.",
  "notif.permission_request.title": "Permission needed",
  "notif.permission_request.speech": "{name} is asking for permission.",
  "notif.usage_reset.title": "{source} limit has reset",
  "notif.usage_reset.body": "The {window} has reset.",
  "notif.usage_reset.speech": "{source}'s {window} has reset.",
  "notif.window.5h": "5-hour window",
  "notif.window.week": "weekly window",
};
