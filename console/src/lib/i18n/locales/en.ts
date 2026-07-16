// English catalog. Typed as Record<keyof typeof ja, string> so tsc fails the build on any
// missing OR extra key — this is our completeness guard in place of a library's tooling.
import type { ja } from "./ja.ts";

export const en: Record<keyof typeof ja, string> = {
  // --- common / settings labels ---
  "common.just_now": "just now",
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

  // --- shared toggles / font names ---
  "common.on": "On",
  "common.off": "Off",
  "font.sys_mono": "System mono",
  "font.sys": "System",
  "font.serif": "Serif",
  "font.mincho": "Mincho (serif)",
  "font.gothic": "Gothic (sans)",

  // --- icon sets ---
  "iconset.vscode": "VS Code Icons (color)",
  "iconset.material": "Material (color)",
  "iconset.devicon": "Devicon (color)",
  "iconset.seti": "Seti (monochrome, tinted by type)",

  // --- surface colors ---
  "surface_color.default": "Default",
  "surface_color.slate": "Slate",
  "surface_color.blue": "Blue",
  "surface_color.green": "Green",
  "surface_color.purple": "Purple",
  "surface_color.warm": "Warm",

  // --- surface targets (short = 外観 popover, long = settings row) ---
  "surface.topbar.short": "Top bar",
  "surface.topbar.long": "Top bar background",
  "surface.leftpane.short": "Left pane",
  "surface.leftpane.long": "Left pane background",
  "surface.viewer.short": "Viewer",
  "surface.viewer.long": "File viewer background",
  "surface.session.short": "Session",
  "surface.session.long": "Session background",
  "surface.assistant.short": "Assistant",
  "surface.assistant.long": "Assistant background",

  // --- mirror send key ---
  "mirror_send.mod_enter": "Send with Ctrl+Enter",
  "mirror_send.enter": "Send with Enter",

  // --- display settings ---
  "display.color_theme": "Color theme",
  "display.theme": "Theme",
  "display.session_theme": "Session theme",
  "display.assistant_theme": "Assistant theme",
  "display.region_theme_note":
    "The session (mirror) and the assistant chat can use their own theme (dark/light), separate from the app itself (“Match app” follows the app). You can also set each one's background color below.",
  "display.terminal": "Terminal",
  "display.font": "Font",
  "display.font_size": "Font size",
  "display.file_viewer": "File viewer",
  "display.tab_width": "Tab width",
  "display.line_numbers": "Line numbers",
  "display.wrap": "Wrap",
  "display.minimap": "Minimap",
  "display.session_mirror": "Session (Markdown mirror)",
  "display.send_key": "Send key",
  "display.send_note_enter": "Enter sends, Shift+Enter for a newline.",
  "display.send_note_mod": "Ctrl+Enter (⌘+Enter) sends, Enter for a newline. For phones.",
  "display.reader_view": "Reader view",
  "display.file_icons": "File icons",
  "display.icon_set": "Icon set",
  "display.preview": "Preview",
  "display.smaller": "Smaller",
  "display.larger": "Larger",
};
