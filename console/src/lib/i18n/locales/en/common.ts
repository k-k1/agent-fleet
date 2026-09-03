// English カタログ / ドメイン: common
// キー接頭辞: common, ui, theme, region_theme, color, surface, surface_color, font, iconset, out_lang, state, exit, popout, swipe, pane, topbar, wset, onb, wsstart
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { common as jaCommon } from "../ja/common.ts";

export const common: Record<keyof typeof jaCommon, string> = {
  // --- common / settings labels ---
  "common.just_now": "just now",
  "theme.dark": "Dark",
  "theme.light": "Light",
  "region_theme.inherit": "Match app",

  // --- shared toggles / font names ---
  "common.on": "On",
  "common.off": "Off",
  "common.move_up": "Move up",
  "common.move_down": "Move down",
  "font.sys_mono": "System mono",
  "font.sys": "System",
  "font.serif": "Serif",
  "font.mincho": "Mincho (serif)",
  "font.gothic": "Gothic (sans)",
  "font.cjk_auto": "Automatic",
  "font.cjk_off": "Latin font",

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
  "surface_color.teal": "Teal",
  "surface_color.rose": "Rose",
  "surface_color.pink": "Pink",
  "surface_color.indigo": "Indigo",
  "surface_color.mono": "Graphite",

  // --- surface targets (short = 外観 popover, long = settings row) ---
  "surface.topbar.short": "Top bar",
  "surface.topbar.long": "Top bar background",
  "surface.leftpane.short": "Left pane",
  "surface.leftpane.long": "Left pane background",
  "surface.viewer.short": "Viewer",
  "surface.viewer.long": "File viewer background",
  "surface.session.short": "Session",
  "surface.session.long": "Session background",
  "surface.shared.short": "Shared",
  "surface.shared.long": "Shared session background",
  "surface.assistant.short": "Assistant",
  "surface.assistant.long": "Assistant background",

  // --- common ---
  "common.close": "Close",

  // --- common (load/start) ---
  "common.loading": "Loading…",
  "common.starting": "Starting…",
  // --- common (save) ---
  "common.save_failed": "Failed to save.",

  // --- common (delete/cancel) ---
  "common.cancel": "Cancel",
  "common.delete": "Delete",
  "common.delete_confirm": "Delete",

  // --- terminal colors (SSM_HOST_COLORS) ---
  "color.auto": "Auto",
  "color.red": "Red",
  "color.orange": "Orange",
  "color.yellow": "Yellow",
  "color.green": "Green",
  "color.teal": "Teal",
  "color.blue": "Blue",
  "color.purple": "Purple",
  "color.pink": "Pink",
  "color.gray": "Gray",

  // --- common (default) ---
  "common.default": "Default",

  // --- common (save/back) ---
  "common.save": "Save",
  "common.back": "Back",
  "common.save_failed_msg": "Failed to save: {msg}",

  // --- assistant output language (OUTPUT_LANGUAGES) ---
  "out_lang.auto": "Match input",
  "out_lang.ja": "Japanese",
  "out_lang.en": "English",

  // === P2: plural infra (tCount) example + <Trans> example. ===
  "common.days_left_one": "{count} day left",
  "common.days_left_other": "{count} days left",
  "common.count_ken_one": "{count} item",
  "common.count_ken_other": "{count} items",

  // === P2 shared: session status chip (lib/sessionview.ts stateInfo). ===
  "state.folder_missing": "Folder missing — can't resume",
  "state.stopped": "Stopped",
  "state.stopped_question": "Stopped · question",
  "state.stopped_plan": "Stopped · approval",
  "state.stopped_permission": "Stopped · permission",
  "state.running": "Running",
  "state.compacting": "Compacting…",
  "state.working": "Working…",
  "state.question": "Question",
  "state.plan": "Plan ready",
  "state.permission": "Awaiting permission",
  "state.blocked": "Limit reached — action needed",
  "state.spend_limit": "Spend limit — needs a raise",
  "state.rate_limited": "Waiting for limit reset",
  "state.rate_limited_at": "Waiting for limit reset · {at}",
  "state.auth_expired": "Login expired — sign in again",
  "state.idle_bg": "Ready · running in background",
  // Wording for when we know WHAT is running (backgroundBusyReason); an absent or
  // unknown reason falls back to the generic line above.
  "state.idle_bg_subagent": "Ready · subagent running",
  "state.idle_bg_shell": "Ready · background command running",
  "state.idle": "Ready",

  // === P2 shared: abnormal-exit label (lib/sessionview.ts exitLabel; hint = tooltip). ===
  "exit.oom.text": "Ended (out of memory)",
  "exit.oom.hint":
    "The process was killed for running out of memory (OOM kill / exit {code}). The workspace may have hit its memory limit.",
  "exit.killed.text": "Force-killed",
  "exit.killed.hint":
    "The process was force-killed with SIGKILL (signal {signal}). Host-wide memory pressure may be the cause.",
  "exit.crashed.text": "Crashed",
  "exit.crashed.hint_signal": "The process crashed on signal {signal}.",
  "exit.crashed.hint_code": "The process exited abnormally (exit code {code}).",
  // Starting dialog (WsStartingDialog — docs/log/35 §35.9-9)
  "wsstart.title": "Starting workspace",
  "wsstart.generic": "Starting…",
  "wsstart.blocked": "Cannot start. Waiting will not help",
  "wsstart.installing_clis": "Installing agent CLIs… (first start only, can take a few minutes)",
  "wsstart.fetching_tool": "Fetching additional tools…",
  "wsstart.toolchain": "Installing toolchain…",
  "wsstart.slot_making_room": "Clearing an unused machine to make space for one your size… (this is the slowest path)",
  "wsstart.slot_creating": "Getting a machine ready for you… (a new one is being started; this takes a few minutes)",
  "wsstart.slot_waking": "Waking your machine…",
  "wsstart.slot_booting": "Waiting for the machine to come up…",
  "wsstart.home_creating": "Creating your home disk… (first start only)",
  "wsstart.home_restoring": "Restoring your home from its saved copy…",
  "wsstart.home_attaching": "Attaching your home disk…",
  "wsstart.hint": "Progress is also recorded in agent.log. Closing this dialog does not stop the start.",

  // === P2 TopBar (app/TopBar.tsx) ===
  "topbar.nav_toggle": "Left panel: click to toggle / double-click to switch mode (push ⇄ overlay)",
  "topbar.tts.stop_off": "Stop reading and turn off",
  "topbar.tts.on": "Voice reading: on (click to turn off)",
  "topbar.tts.off": "Voice reading: off (click to turn on)",
  "topbar.tts.generating": "Generating audio",
  "topbar.tts.speaking": "Reading aloud",
  "topbar.fullscreen_exit": "Exit fullscreen",
  "topbar.fullscreen_enter": "Fullscreen",
  "topbar.reload": "Reload",
  "topbar.appearance_title": "Appearance (layout, theme, colors)",
  "topbar.appearance": "Appearance",
  "topbar.appearance_details": "Details",
  "topbar.appearance_details_title": "Open display settings (fonts, advanced colors, …)",
  "topbar.tenant": "Tenant",
  "topbar.menu": "Menu",
  "topbar.user_guide": "User guide",
  "topbar.guide": "Getting-started guide",
  "topbar.settings": "Settings",
  "topbar.tenant_settings": "Tenant settings",
  "topbar.admin": "Admin",
  "topbar.logout": "Sign out",
  "topbar.build": "Build {label}",
  "topbar.server_version": "Server v{v}",
  "topbar.image_cp": "CP image {ref}",
  "topbar.image_ws": "WS image {ref}",
  "topbar.copy_version": "Copy version details",
  "topbar.host_version": "Agent Fleet v{v}",
  "topbar.update_ready": "Update available · restart to apply v{v}",
  "topbar.update_badge": "Update",
  "topbar.settings_title": "Settings (Display / Workspace / Agents / Git / AWS SSM / MCP)",

  // === P2 small shared words ===
  "common.list_sep": ", ",

  // === P2 modal/row common frequent words (common.cancel/close/delete reuse existing) ===
  "common.send": "Send",
  "common.delete_do": "Delete",
  "common.delete_failed": "Failed to delete",
  "common.send_failed": "Failed to send",
  "common.copy_failed": "Failed to copy",

  // === P2 LayoutMap (features/panes/LayoutMap.tsx) ===
  "pane.map_aria": "Pane layout",
  "pane.layout": "Layout",
  "pane.pane_n": "Pane {ord}",
  "pane.no_session": "No session",
  "pane.empty": "empty",
  "pane.kind.file": "File",
  "pane.kind.scm": "Commit graph",
  "pane.kind.changes": "Changes",
  "pane.kind.commit": "Commit",
  "pane.kind.wtdiff": "File diff",
  "pane.kind.doc": "Document",
  "pane.kind.diff": "Diff",
  "pane.kind.chat": "Chat",
  "pane.kind.read": "Reader view",
  "pane.kind.browser": "Browser",
  "pane.kind.browser_attach": "Chromium operation view",

  // === P2 common (added) ===
  "common.approx": "~{v}",
  "common.focus_pane": "Focus pane {ordinal}",

  // === Phone left-swipe = rotate through running sessions (app/App.tsx) ===
  "swipe.rotated": "{n}/{total} {name}",
  "swipe.rotate_none": "No other running session",

  // === P5 共通 ===
  "common.mid_dot": "·",
  // ロケール別約物（keyHint.ts の hintSuffix と同じ流儀）
  "common.paren": " ({v})",
  "common.detail_sep": ": ",

  // === P5 オンボーディング/ターミナル（OnboardingCard/TerminalView/term） ===
  "onb.ws_first": "Start the workspace first",
  "onb.start_ws": "Start workspace",
  "onb.start_ws_hint": "Brings up your own private container",
  "onb.starting": "Starting…",
  "onb.start": "Start",
  "onb.connect_agent": "Connect an agent",
  "onb.connect_agent_hint": "Sign in to Claude, Codex, or opencode",
  "onb.connect": "Connect",
  "onb.connect_git": "Connect a git provider",
  "onb.optional": "Optional",
  "onb.connect_git_hint": "Connect to clone / push private repositories",
  "onb.clone_start": "Clone a repository and start a session",
  "onb.clone_start_hint": "Clone and launch together from “Get started”",
  "onb.get_started": "Get started",
  "onb.which_start": "Where do you want to start? — you can use both later",
  "onb.tile_chat_title": "Ask AI a question or for a translation",
  "onb.tile_chat_desc": "A throwaway chat. No git or terminal needed — ready to use.",
  "onb.chat_needs_setup": "Available once you finish the two steps above",
  "onb.start_chat": "Start chatting",
  "onb.tile_dev_title": "Develop in a repository",
  "onb.tile_dev_desc": "Connect git, clone a repository, and launch an AI session.",
  "onb.collapse_steps": "Collapse steps",
  "onb.to_dev_setup": "Go to dev setup",
  "onb.welcome": "Welcome to Agent Fleet",
  "onb.welcome_sub": "Two steps first, then just pick your goal",
  "onb.later": "Later",
  "onb.guide_title": "Getting started guide",
  "onb.guide_sub": "Completed items are checked off automatically",
  "onb.session": "Session",
  "onb.session_disconnected": "No session connected",
  "onb.resuming": "Resuming…",
  "onb.resume_this_session": "Resume this session",
  "onb.ws_stopped": "Workspace stopped",
  "onb.resume": "Resume",
  "onb.paste_confirm_title": "Paste into the terminal?",
  "onb.paste_chars": "{count} characters from the clipboard",
  "onb.paste_lines": " ({lines} lines)",
  "onb.paste_suffix": " will be pasted.",
  "onb.paste_newline_warn": " It contains newlines, so a shell may execute it directly.",
  "onb.paste_confirm": "Paste",
  // Disconnect notice written straight into the terminal grid (term.ts)
  "onb.term_disconnected": "[disconnected]",
  "onb.rtt_unit": "ms",
  "onb.rtt_title":
    "Terminal round trip (median {med}ms / worst {max}ms / last {n} samples).\nMeasured over the same path and the same frames your keystrokes take, browser ↔ workspace. The PTY/tmux hop itself is under a millisecond, so this is very nearly the echo delay you feel.",
  "onb.term_session_stopped": "[this session is stopped — use Resume at the bottom right to bring it back]",
  "wset.all": "All",
  "wset.bar_title": "Scope the left pane to a working set",
  "wset.manage": "Manage groups…",
  "wset.menu_caption": "Working sets",
  "wset.manage_title": "Working sets",
  "wset.new_ph": "New group name",
  "wset.create": "Create",
  "wset.delete": "Delete group",
  "wset.delete_title": "Delete working set",
  "wset.delete_confirm": "Delete the group \"{name}\"? Its members (repos, chats, sessions) are not touched.",
  "wset.empty_hint": "No working sets yet. Create one to assign repos, chats and sessions per project and switch what the left pane shows.",
  "wset.no_repos": "No repositories in this group (assign from a row's right-click menu)",
  "wset.name_aria": "Group name",
  "wset.row_counts": "Repos {repos} / chats {convs} / sessions {sessions} / schedules {schedules}",
  "wset.none_hint": "No working sets yet (create one at the top of the left pane)",
  "wset.no_schedules": "No schedules in this group",
  "wset.derived_hint": "Included automatically via its repo / conversation membership",

  // === P5 共有 UI（ui/* ・panes・App/TopBar・useUpdateCheck・agentModels・workspace・WhichKey） ===
  "ui.sep": "·",
  "ui.assistant": "Assistant",
  "ui.repositories": "Repositories",
  "ui.files": "Files",
  "ui.starts_when_workspace_running": "Appears once the workspace is running.",
  "ui.filter_models": "Filter models…",
  "ui.filter_kind_models": "Filter {kind} models",
  "ui.kind_model": "{kind} model",
  "ui.claude_registered_model": "Select a registered model",
  "ui.select_from_count": "Select from {count}",
  "ui.no_matching_models": "No matching models",
  // Shown when a dynamic kind's catalog resolved to no selectable model. It must stay
  // true for EVERY reason that produces it — not signed in, signed in but unable to
  // reach the provider, a plan that only offers the default (Copilot Free is Auto-only,
  // where empty is correct), or everything excluded in settings. The Agent does not say
  // which, so this must not name a cause.
  "ui.model_default_only": "Only the default model is available (check this agent's connection and plan).",
  "ui.count_items": "{count} items",
  "ui.cancel": "Cancel",
  "ui.run": "Run",
  "ui.running": "Running…",
  "ui.processing": "Processing…",
  "ui.close": "Close",
  "ui.ai_related": "AI-related",
  "ui.secret": "Secret",
  "ui.confirm_continue": "Continue?",
  "ui.pane_swap_hint": "Pane {ordinal} — drag to swap with another pane",
  "ui.drag_to_swap": "Drag to swap",
  "ui.unwrap_lines": "Disable line wrap",
  "ui.wrap_lines": "Wrap lines",
  "ui.close_pane_hint": "Close this pane (middle-click / Ctrl+click to close directly)",
  "ui.close_tab_hint": "Close this tab",
  "ui.popout_pane_hint": "Open in a new tab (moves this pane)",
  "ui.popout_expand": "Expand to full console",
  "popout.blocked": "Could not open the new tab. Check the browser's pop-up blocker",
  "popout.stale_link": "This pop-out link is no longer valid — opened the normal console",
  "popout.cannot": "This pane cannot be moved to its own tab",
  "ui.find_in_pane": "Find in pane",
  "ui.find_prev": "Previous match (Shift+Enter)",
  "ui.find_next": "Next match (Enter)",
  "ui.close_find": "Close find (Esc)",
  "ui.next_key": "Next key",
  "ui.wk_groups": "Submenus",
  "ui.wk_actions": "Actions",
  "ui.wk_back": "back",
  "ui.wk_cancel": "cancel",
  "ui.new_version_available": "A new version is available",
  "ui.update_sessions_safe": "Updating does not stop running sessions.",
  "ui.update_backend_note":
    "The backend was updated too. Applying it needs a workspace stop→start whenever it suits you (running sessions stop, and can be resumed later).",
  "ui.update": "Update",
  "ui.recreate_failed": "Recreate failed",
  "ui.cleanup_failed": "Cleanup failed",
  "ui.default": "Default",
  "ui.default_with": "Default ({effort})",
  "ui.default_claude_xhigh": "Default (Claude Code = xhigh)",
};
